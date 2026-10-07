// SPDX-License-Identifier: MPL-2.0
import { blameLine, readCommit } from '@bugsnitch/git';
import type { GitRunOptions } from '@bugsnitch/git';
import type { Investigation } from '@bugsnitch/shared';

export interface LineInvestigationInput {
  repository: string;
  path: string;
  line: number;
  contents: string;
}

/** Trace a current saved line, then enrich committed provenance with its full message. */
export async function investigateLine(
  input: LineInvestigationInput,
  options: GitRunOptions = {},
): Promise<Investigation> {
  const provenance = await blameLine(
    input.repository,
    input.path,
    input.line,
    input.contents,
    options,
  );
  const commit = provenance.uncommitted
    ? null
    : await readCommit(input.repository, provenance.hash, options);
  return { repository: input.repository, provenance, commit };
}
