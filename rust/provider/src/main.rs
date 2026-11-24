// SPDX-License-Identifier: MPL-2.0
mod client;
mod pulls;
mod queue;
mod repo;

use client::{Client, Error, Result};
use repo::{Repository, Scope};
use serde::Deserialize;
use serde_json::{Value, json};
use std::{
    env,
    io::{self, Read, Write},
    process::ExitCode,
};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Frame {
    version: u8,
    request: Command,
}
#[derive(Deserialize)]
#[serde(tag = "op", rename_all = "snake_case", deny_unknown_fields)]
enum Command {
    Open {
        installation: i64,
        repo: i64,
        name: String,
        scope: Scope,
    },
    Identity,
    Get {
        number: i64,
    },
    Create {
        title: String,
        body: String,
    },
    Find {
        marker: String,
        since: String,
    },
    CheckDraft {
        head: String,
        candidate: String,
        target: String,
        base: String,
        number: i64,
        updated: String,
    },
    FindDraft {
        head: String,
        target: String,
        marker: String,
    },
    CreateDraft {
        head: String,
        target: String,
        title: String,
        body: String,
    },
    QueueSubject {
        number: i64,
        target: String,
    },
    QueueGroup {
        group: String,
        group_ref: String,
        base: String,
        head: String,
    },
    QueueCheck {
        label: String,
        sha: String,
        request: String,
    },
    Enqueue {
        number: i64,
        base: String,
        target: String,
        head: String,
    },
    Inspect {
        number: i64,
        issue: i64,
    },
}

fn call(r: &Repository, cmd: Command) -> Result<Value> {
    match cmd {
        Command::Identity if r.scope == Scope::Discover => Ok(json!(r.id)),
        Command::Get { number } if matches!(r.scope, Scope::Issues | Scope::ReadIssue) => {
            Ok(json!(r.get(number)?))
        }
        Command::Create { title, body } if r.scope == Scope::Issues => {
            Ok(json!(r.create(&title, &body)?))
        }
        Command::Find { marker, since } if r.scope == Scope::Issues => {
            Ok(json!(r.find(&marker, &since)?))
        }
        Command::CheckDraft {
            head,
            candidate,
            target,
            base,
            number,
            updated,
        } if r.scope == Scope::Draft => {
            r.check_draft(&head, &candidate, &target, &base, number, &updated)?;
            Ok(Value::Null)
        }
        Command::FindDraft {
            head,
            target,
            marker,
        } if r.scope == Scope::Draft => Ok(json!(r.find_draft(&head, &target, &marker)?)),
        Command::CreateDraft {
            head,
            target,
            title,
            body,
        } if r.scope == Scope::Draft => Ok(json!(r.create_draft(&head, &target, &title, &body)?)),
        Command::QueueSubject { number, target }
            if matches!(r.scope, Scope::QueueRead | Scope::Queue) =>
        {
            r.queue_subject(number, &target)
        }
        Command::QueueGroup {
            group,
            group_ref,
            base,
            head,
        } if matches!(r.scope, Scope::QueueRead | Scope::Queue) => {
            Ok(json!(r.queue_group(&group, &group_ref, &base, &head)?))
        }
        Command::QueueCheck {
            sha,
            request,
            label,
        } if r.scope == Scope::Queue => {
            r.queue_check(&sha, &request, &label)?;
            Ok(Value::Null)
        }
        Command::Enqueue {
            number,
            base,
            target,
            head,
        } if r.scope == Scope::Queue => {
            r.enqueue(number, &base, &target, &head)?;
            Ok(Value::Null)
        }
        Command::Inspect { number, issue } if r.scope == Scope::Inspection => {
            r.inspect(number, issue)
        }
        _ => Err(Error::Invalid),
    }
}
fn serve(fixture: Option<&str>) -> io::Result<()> {
    let mut stdin = io::stdin().lock();
    let mut stdout = io::stdout().lock();
    let mut repo = None;
    loop {
        let mut head = [0; 4];
        if stdin.read(&mut head[..1])? == 0 {
            return Ok(());
        }
        stdin.read_exact(&mut head[1..])?;
        let n = u32::from_be_bytes(head) as usize;
        if n == 0 || n > 256 * 1024 {
            return Err(io::Error::other("invalid frame"));
        }
        let mut body = vec![0; n];
        stdin.read_exact(&mut body)?;
        let result = serde_json::from_slice::<Frame>(&body)
            .map_err(|_| Error::Invalid)
            .and_then(|frame| {
                if frame.version != 1 {
                    return Err(Error::Invalid);
                }
                match (repo.as_ref(), frame.request) {
                    (
                        None,
                        Command::Open {
                            installation,
                            repo: id,
                            name,
                            scope,
                        },
                    ) => {
                        let client = Client::new(fixture)?;
                        repo = Some(Repository::open(client, installation, id, name, scope)?);
                        Ok(Value::Null)
                    }
                    (Some(r), command) => call(r, command),
                    _ => Err(Error::Invalid),
                }
            });
        let output = match result {
            Ok(v) => json!({"version":1,"value":v}),
            Err(e) => json!({"version":1,"error":e}),
        };
        let bytes = serde_json::to_vec(&output)?;
        if bytes.len() > 1024 * 1024 {
            return Err(io::Error::other("invalid response size"));
        }
        stdout.write_all(&(bytes.len() as u32).to_be_bytes())?;
        stdout.write_all(&bytes)?;
        stdout.flush()?;
    }
}
fn main() -> ExitCode {
    let args: Vec<String> = env::args().skip(1).collect();
    let ok = match args.as_slice() {
        [mode] if mode == "--check-key" => Client::new(None).is_ok(),
        [] => serve(None).is_ok(),
        [mode, origin] if mode == "--loopback-fixture" => serve(Some(origin)).is_ok(),
        _ => false,
    };
    if ok {
        ExitCode::SUCCESS
    } else {
        ExitCode::FAILURE
    }
}
