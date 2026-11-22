// SPDX-License-Identifier: MPL-2.0
//! Shared by incident intake and explicitly shared runner artifacts.
//! The expressions follow internal/incidents.Redact, including RE2's ASCII
//! whitespace and word boundaries. The order of replacements is significant.
use regex::Regex;
use std::sync::LazyLock;

static PRIVATE_BLOCK: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?s)-----BEGIN [^-]+-----.*?(?:-----END [^-]+-----|$)").unwrap());
static SENSITIVE_LINE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?im)^.*(?:authorization|cookie|password|passwd|api[ _-]?key|access[ _-]?token|refresh[ _-]?token|client[ _-]?secret|secret[ _-]?key|bearer)[\t\n\f\r ]*[:= ][\t\n\f\r ]*.+$").unwrap()
});
static CREDENTIAL: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)(?:https?://[^\t\n\f\r <>]+|(?:gh[pousr]_|github_pat_|sk-|xox[baprs]-)[a-z0-9_-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}|(?-u:\b)[a-z0-9+/_=-]{32,}(?-u:\b))").unwrap()
});
static CONTROL: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"[\p{Cf}\p{Cc}&&[^\n\t]]").unwrap());

pub fn redact(text: &str) -> String {
    let text = PRIVATE_BLOCK.replace_all(text, "[redacted]");
    let text = SENSITIVE_LINE.replace_all(&text, "[redacted]");
    let text = CREDENTIAL.replace_all(&text, "[redacted]");
    CONTROL.replace_all(&text, "").trim().to_owned()
}

pub fn shareable(text: &str) -> bool {
    redact(text) == text
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn artifact_sharing_and_ingress_have_the_same_boundary() {
        assert!(shareable("a\n\tb"));
        assert!(shareable("normal café 🍉"));
        for original in [
            "x\u{202e}z",
            "control\u{7f}",
            " trailing ",
            "Authorization: Bearer NEVER-SHARE",
            "a\nCOOKIE = private\nb",
            "https://localhost/route?secret=value",
            "a@b.example",
            "github_pat_AbCde_34",
            "-----BEGIN A-----\nprivate\n-----END A-----",
            "abcdefghijklmnopqrstuvwxyz1234567890",
            "eyJabc.def.Ghi",
        ] {
            assert!(!shareable(original));
        }
        assert_eq!(redact("a\u{202e}\0b\n\tc"), "ab\n\tc");
        assert_eq!(
            redact("first\nAuthorization: Bearer SYNTHETIC-marker\nlast"),
            "first\n[redacted]\nlast"
        );
        assert!(shareable("Prices include tax"));
    }
}
