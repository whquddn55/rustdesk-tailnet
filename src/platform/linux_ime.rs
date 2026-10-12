//! IBus input source selection for the soft keyboard's Hangul keys
//! (`tailnet::ime_mode_request`).
//!
//! ibus-hangul keeps its own Hangul/Latin mode inside the `hangul` engine and does not
//! expose it, so Korean is selected by starting a fresh `hangul` engine, whose mode is
//! `initial-input-mode`, and toggling once when that mode is Latin.

use hbb_common::{bail, log, ResultType};
use std::process::Command;

const HANGUL_ENGINE: &str = "hangul";
const LATIN_ENGINE: &str = "xkb:us::eng";

fn run(program: &str, args: &[&str]) -> ResultType<String> {
    let out = Command::new(program).args(args).output()?;
    if !out.status.success() {
        bail!(
            "{} {:?}: {}",
            program,
            args,
            String::from_utf8_lossy(&out.stderr).trim()
        );
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().to_owned())
}

fn gsetting(schema: &str, key: &str) -> String {
    run("gsettings", &["get", schema, key]).unwrap_or_default()
}

/// The engine to use for English: the first preloaded engine that is not Hangul.
fn latin_engine() -> String {
    let preload = gsetting("org.freedesktop.ibus.general", "preload-engines");
    preload
        .trim_matches(|c| c == '[' || c == ']')
        .split(',')
        .map(|e| e.trim().trim_matches('\'').to_owned())
        .find(|e| !e.is_empty() && !e.contains(HANGUL_ENGINE))
        .unwrap_or_else(|| LATIN_ENGINE.to_owned())
}

/// Selects Korean or English. Returns true when the caller must press the Hangul key
/// once to bring a fresh `hangul` engine from Latin to Hangul mode.
pub fn select_korean(korean: bool) -> ResultType<bool> {
    let current = run("ibus", &["engine"])?;
    let latin = latin_engine();
    if !korean {
        if current.contains(HANGUL_ENGINE) {
            run("ibus", &["engine", &latin])?;
        }
        return Ok(false);
    }
    if current.contains(HANGUL_ENGINE) {
        // Its Hangul/Latin mode is unknown: start over from a fresh engine.
        run("ibus", &["engine", &latin])?;
    }
    run("ibus", &["engine", HANGUL_ENGINE])?;
    let schema = "org.freedesktop.ibus.engine.hangul";
    let latin_first = gsetting(schema, "initial-input-mode") != "'hangul'"
        && gsetting(schema, "disable-latin-mode") != "true";
    log::debug!("ibus: {} -> hangul, toggle: {}", current, latin_first);
    Ok(latin_first)
}
