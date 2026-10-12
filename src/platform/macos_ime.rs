//! Korean input source toggle across platforms (see `ime_toggle_key_events` in keyboard.rs).
//!
//! Controller side: find which keys toggle the input source on this Mac by reading the user's
//! System Settings, so no manual setup is needed.
//! Controlled side: switch between the Korean and a non-Korean input source with the TIS API,
//! whatever shortcut this Mac uses.

use base::message_proto::{key_event, ControlKey, KeyEvent, KeyboardMode};
use core_foundation::{
    array::{CFArrayGetCount, CFArrayGetValueAtIndex, CFArrayRef},
    base::{CFRelease, CFTypeRef, TCFType},
    boolean::kCFBooleanTrue,
    dictionary::CFDictionaryRef,
    string::{CFString, CFStringRef},
};
use hbb_common::log;
use std::{
    collections::HashMap,
    ffi::c_void,
    path::PathBuf,
    sync::Mutex,
    time::{Duration, Instant},
};

pub const KVK_CAPS_LOCK: u32 = 0x39;
const KVK_FUNCTION: u32 = 0x3f;

// Modifier masks used in com.apple.symbolichotkeys parameters.
const MASK_SHIFT: u64 = 0x20000;
const MASK_CONTROL: u64 = 0x40000;
const MASK_OPTION: u64 = 0x80000;
const MASK_COMMAND: u64 = 0x100000;

// "Select the previous input source" / "Select next source in Input menu".
const HOTKEY_PREV_SOURCE: &str = "60";
const HOTKEY_NEXT_SOURCE: &str = "61";

const REFRESH: Duration = Duration::from_secs(5);

#[derive(Debug, Default, Clone, PartialEq)]
struct Toggles {
    // (key code, modifier mask)
    shortcuts: Vec<(u32, u64)>,
    caps_lock: bool,
    globe: bool,
}

lazy_static::lazy_static! {
    static ref TOGGLES: Mutex<Option<(Instant, Toggles)>> = Mutex::new(None);
    static ref LAST_SOURCE: Mutex<HashMap<bool, String>> = Mutex::new(HashMap::new());
    static ref CAPS_LOCK_LAST: Mutex<Option<Instant>> = Mutex::new(None);
}

pub fn is_input_source_shortcut(
    code: u32,
    alt: bool,
    ctrl: bool,
    shift: bool,
    command: bool,
) -> bool {
    let toggles = current_toggles();
    match code {
        KVK_CAPS_LOCK => toggles.caps_lock,
        KVK_FUNCTION => toggles.globe,
        _ => {
            let pressed = (if shift { MASK_SHIFT } else { 0 })
                | (if ctrl { MASK_CONTROL } else { 0 })
                | (if alt { MASK_OPTION } else { 0 })
                | (if command { MASK_COMMAND } else { 0 });
            toggles
                .shortcuts
                .iter()
                .any(|(key, mask)| *key == code && *mask == pressed)
        }
    }
}

/// Caps Lock arrives as flag changes: one event per tap while it latches, a press/release pair
/// when it does not. Count events closer than this as the same tap.
pub fn caps_lock_toggle_due() -> bool {
    const SAME_TAP: Duration = Duration::from_millis(300);
    let mut last = CAPS_LOCK_LAST.lock().unwrap();
    let due = last.map_or(true, |at| at.elapsed() >= SAME_TAP);
    *last = Some(Instant::now());
    due
}

fn current_toggles() -> Toggles {
    let mut cache = TOGGLES.lock().unwrap();
    if let Some((at, toggles)) = cache.as_ref() {
        if at.elapsed() < REFRESH {
            return toggles.clone();
        }
    }
    let toggles = read_toggles();
    if cache.as_ref().map(|(_, t)| t) != Some(&toggles) {
        log::info!("input source toggles: {:?}", toggles);
    }
    *cache = Some((Instant::now(), toggles.clone()));
    toggles
}

fn preferences(domain: &str) -> Option<plist::Dictionary> {
    let path = PathBuf::from(std::env::var("HOME").ok()?)
        .join("Library/Preferences")
        .join(format!("{domain}.plist"));
    plist::Value::from_file(path).ok()?.into_dictionary()
}

fn read_toggles() -> Toggles {
    let mut toggles = Toggles::default();
    let hotkeys = preferences("com.apple.symbolichotkeys")
        .and_then(|d| d.get("AppleSymbolicHotKeys")?.as_dictionary().cloned());
    for (id, default) in [
        (HOTKEY_PREV_SOURCE, (49, MASK_CONTROL)),
        (HOTKEY_NEXT_SOURCE, (49, MASK_CONTROL | MASK_OPTION)),
    ] {
        match hotkeys.as_ref().and_then(|h| h.get(id)?.as_dictionary()) {
            Some(entry) => {
                if let Some(shortcut) = parse_hotkey(entry) {
                    toggles.shortcuts.push(shortcut);
                }
            }
            // Never customized: macOS uses its default shortcut.
            None => toggles.shortcuts.push(default),
        }
    }
    toggles.caps_lock = preferences(".GlobalPreferences")
        .and_then(|d| d.get("TISRomanSwitchState")?.as_signed_integer())
        == Some(1);
    toggles.globe = preferences("com.apple.HIToolbox")
        .and_then(|d| d.get("AppleFnUsageType")?.as_signed_integer())
        == Some(1);
    toggles
}

fn parse_hotkey(entry: &plist::Dictionary) -> Option<(u32, u64)> {
    let enabled = match entry.get("enabled")? {
        plist::Value::Boolean(b) => *b,
        v => v.as_signed_integer()? != 0,
    };
    if !enabled {
        return None;
    }
    let params = entry
        .get("value")?
        .as_dictionary()?
        .get("parameters")?
        .as_array()?;
    let code = params.get(1)?.as_signed_integer()?;
    let mask = params.get(2)?.as_signed_integer()? as u64;
    let mask = mask & (MASK_SHIFT | MASK_CONTROL | MASK_OPTION | MASK_COMMAND);
    Some((u32::try_from(code).ok()?, mask))
}

/// Controlled side: a legacy `Hangul` key from the controller switches the input source.
/// Returns true when the event was handled here.
pub fn handle_ime_toggle(evt: &KeyEvent) -> bool {
    if let Some(korean) = crate::tailnet::ime_mode_request(evt) {
        if let Err(e) = select_korean_input_source(korean) {
            log::warn!("failed to select input source: {}", e);
        }
        return true;
    }
    if evt.mode.enum_value() != Ok(KeyboardMode::Legacy) {
        return false;
    }
    let Some(key_event::Union::ControlKey(ck)) = &evt.union else {
        return false;
    };
    if ck.enum_value() != Ok(ControlKey::Hangul) {
        return false;
    }
    if evt.down {
        if let Err(e) = toggle_korean_input_source() {
            log::warn!("failed to toggle input source: {}", e);
        }
    }
    true
}

type TISInputSourceRef = *mut c_void;

#[link(name = "Carbon", kind = "framework")]
extern "C" {
    fn TISCopyCurrentKeyboardInputSource() -> TISInputSourceRef;
    fn TISCreateInputSourceList(
        properties: CFDictionaryRef,
        include_all_installed: u8,
    ) -> CFArrayRef;
    fn TISSelectInputSource(source: TISInputSourceRef) -> i32;
    fn TISGetInputSourceProperty(source: TISInputSourceRef, key: CFStringRef) -> *const c_void;
    static kTISPropertyInputSourceID: CFStringRef;
    static kTISPropertyInputSourceCategory: CFStringRef;
    static kTISPropertyInputSourceIsSelectCapable: CFStringRef;
    static kTISPropertyInputSourceIsASCIICapable: CFStringRef;
    static kTISPropertyInputSourceLanguages: CFStringRef;
    static kTISCategoryKeyboardInputSource: CFStringRef;
}

unsafe fn string_property(source: TISInputSourceRef, key: CFStringRef) -> Option<String> {
    let value = TISGetInputSourceProperty(source, key);
    if value.is_null() {
        return None;
    }
    Some(CFString::wrap_under_get_rule(value as CFStringRef).to_string())
}

unsafe fn bool_property(source: TISInputSourceRef, key: CFStringRef) -> bool {
    let value = TISGetInputSourceProperty(source, key);
    !value.is_null() && value == kCFBooleanTrue as *const c_void
}

unsafe fn is_korean(source: TISInputSourceRef) -> bool {
    let languages = TISGetInputSourceProperty(source, kTISPropertyInputSourceLanguages);
    if !languages.is_null() && CFArrayGetCount(languages as CFArrayRef) > 0 {
        let first = CFArrayGetValueAtIndex(languages as CFArrayRef, 0);
        if CFString::wrap_under_get_rule(first as CFStringRef).to_string() == "ko" {
            return true;
        }
    }
    string_property(source, kTISPropertyInputSourceID).is_some_and(|id| id.contains("Korean"))
}

fn select_korean_input_source(korean: bool) -> hbb_common::ResultType<()> {
    let current_korean = unsafe {
        let current = TISCopyCurrentKeyboardInputSource();
        if current.is_null() {
            hbb_common::bail!("no current input source");
        }
        let k = is_korean(current);
        CFRelease(current as CFTypeRef);
        k
    };
    if current_korean == korean {
        return Ok(());
    }
    toggle_korean_input_source()
}

fn toggle_korean_input_source() -> hbb_common::ResultType<()> {
    unsafe {
        let current = TISCopyCurrentKeyboardInputSource();
        if current.is_null() {
            hbb_common::bail!("no current input source");
        }
        let to_korean = !is_korean(current);
        if let Some(id) = string_property(current, kTISPropertyInputSourceID) {
            LAST_SOURCE.lock().unwrap().insert(!to_korean, id);
        }
        CFRelease(current as CFTypeRef);

        let list = TISCreateInputSourceList(std::ptr::null(), 0);
        if list.is_null() {
            hbb_common::bail!("no enabled input sources");
        }
        let remembered = LAST_SOURCE.lock().unwrap().get(&to_korean).cloned();
        let mut target: Option<TISInputSourceRef> = None;
        for i in 0..CFArrayGetCount(list) {
            let source = CFArrayGetValueAtIndex(list, i) as TISInputSourceRef;
            let keyboard = string_property(source, kTISPropertyInputSourceCategory)
                == Some(CFString::wrap_under_get_rule(kTISCategoryKeyboardInputSource).to_string());
            if !keyboard
                || !bool_property(source, kTISPropertyInputSourceIsSelectCapable)
                || is_korean(source) != to_korean
                || (!to_korean && !bool_property(source, kTISPropertyInputSourceIsASCIICapable))
            {
                continue;
            }
            if remembered.is_some()
                && string_property(source, kTISPropertyInputSourceID) == remembered
            {
                target = Some(source);
                break;
            }
            target.get_or_insert(source);
        }
        let status = target.map(|source| TISSelectInputSource(source));
        CFRelease(list as CFTypeRef);
        match status {
            Some(0) => Ok(()),
            Some(err) => hbb_common::bail!("TISSelectInputSource failed: {}", err),
            None => hbb_common::bail!(
                "no enabled {} input source",
                if to_korean { "Korean" } else { "non-Korean" }
            ),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn hotkey(enabled: bool, code: i64, mask: i64) -> plist::Dictionary {
        let mut value = plist::Dictionary::new();
        value.insert(
            "parameters".into(),
            plist::Value::Array(vec![65535.into(), code.into(), mask.into()]),
        );
        let mut entry = plist::Dictionary::new();
        entry.insert("enabled".into(), enabled.into());
        entry.insert("value".into(), plist::Value::Dictionary(value));
        entry
    }

    #[test]
    fn parses_input_source_hotkeys() {
        // F13 without modifiers, as stored on our Macs.
        assert_eq!(parse_hotkey(&hotkey(true, 105, 0)), Some((105, 0)));
        // Ctrl+Option+Space; the fn bit (0x800000) is ignored.
        assert_eq!(
            parse_hotkey(&hotkey(true, 49, 786432 | 0x800000)),
            Some((49, MASK_CONTROL | MASK_OPTION))
        );
        assert_eq!(parse_hotkey(&hotkey(false, 105, 0)), None);
    }
}
