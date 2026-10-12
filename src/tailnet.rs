//! Tailnet-only build: every peer, server and relay is reached over Tailscale.
//!
//! The socket layer in `hbb_common::tailnet` refuses non-Tailscale addresses. This module turns
//! off the paths that bypass it (UDP/IPv6 punch, KCP, WebRTC, websocket, LAN discovery), keeps
//! direct IP access on, and filters incoming connections and HTTP requests.

use base::config::keys;
use hbb_common::{
    config::{
        DEFAULT_DISPLAY_SETTINGS, DEFAULT_SETTINGS, OVERWRITE_LOCAL_SETTINGS, OVERWRITE_SETTINGS,
    },
    log, tailnet,
};
use std::net::{IpAddr, SocketAddr};

pub const ENFORCED: bool = true;

pub fn init() {
    // Only the API server is known up front; after login it pushes the ID server, relay
    // and key of the account (heartbeat "strategy", hbbs_http/sync.rs).
    if let Some(api_server) = option_env!("TAILNET_API_SERVER").filter(|s| !s.is_empty()) {
        if let Ok(mut settings) = DEFAULT_SETTINGS.write() {
            settings
                .entry(keys::OPTION_API_SERVER.to_owned())
                .or_insert_with(|| api_server.to_owned());
        }
    }
    if let Ok(mut settings) = OVERWRITE_SETTINGS.write() {
        settings.insert(keys::OPTION_DIRECT_SERVER.to_owned(), "Y".to_owned());
        settings.insert(keys::OPTION_ENABLE_LAN_DISCOVERY.to_owned(), "N".to_owned());
        settings.insert(keys::OPTION_ALLOW_WEBSOCKET.to_owned(), "N".to_owned());
    }
    if let Ok(mut settings) = OVERWRITE_LOCAL_SETTINGS.write() {
        for key in [
            keys::OPTION_ENABLE_UDP_PUNCH,
            keys::OPTION_ENABLE_IPV6_PUNCH,
            keys::OPTION_ENABLE_WEBRTC,
        ] {
            settings.insert(key.to_owned(), "N".to_owned());
        }
    }
    // H.264 has the widest hardware encoder support; "auto" often lands on software VP9.
    if let Ok(mut settings) = DEFAULT_DISPLAY_SETTINGS.write() {
        settings
            .entry(keys::OPTION_CODEC_PREFERENCE.to_owned())
            .or_insert_with(|| "h264".to_owned());
    }
}

pub fn check_incoming(addr: &SocketAddr) -> hbb_common::ResultType<()> {
    tailnet::check_addr(addr)
}

/// Resolves host names to their Tailscale addresses only, so HTTP never leaves the tailnet.
pub struct HttpResolver;

impl reqwest::dns::Resolve for HttpResolver {
    fn resolve(&self, name: reqwest::dns::Name) -> reqwest::dns::Resolving {
        Box::pin(resolve_tailnet(name.as_str().to_owned()))
    }
}

async fn resolve_tailnet(
    host: String,
) -> Result<reqwest::dns::Addrs, Box<dyn std::error::Error + Send + Sync>> {
    let addrs: Vec<SocketAddr> = hbb_common::tokio::net::lookup_host((host.as_str(), 0))
        .await?
        .filter(tailnet::is_allowed_addr)
        .collect();
    if addrs.is_empty() {
        log::warn!("tailnet-only: refused HTTP to {}", host);
        return Err(format!("tailnet-only: {host} has no Tailscale address").into());
    }
    Ok(Box::new(addrs.into_iter()))
}

/// IP-literal URLs skip the resolver, so send the ones outside the tailnet to a dead
/// loopback port: the request fails instead of leaving the tailnet.
pub fn http_block_proxy() -> reqwest::Proxy {
    reqwest::Proxy::custom(|url| {
        let ip: IpAddr = url
            .host_str()?
            .trim_start_matches('[')
            .trim_end_matches(']')
            .parse()
            .ok()?;
        if tailnet::is_allowed_ip(ip) {
            return None;
        }
        log::warn!("tailnet-only: refused HTTP to {}", ip);
        Some("http://127.0.0.1:9")
    })
}

/// The heartbeat that brings the account's ID server, relay and key runs inside the
/// controlled-side service, which a phone used only as a controller never starts.
/// Fetch them once right away: after login and when the app starts logged in.
#[cfg(target_os = "android")]
pub fn sync_account_settings() {
    std::thread::spawn(|| {
        if let Err(e) = mobile::fetch_account_settings() {
            log::warn!("failed to fetch account settings: {}", e);
        }
    });
}

#[cfg(target_os = "android")]
mod mobile {
    use crate::hbbs_http::sync::{handle_config_options, StrategyOptions};
    use hbb_common::{
        config::{Config, LocalConfig},
        log, tokio, ResultType,
    };

    #[tokio::main(flavor = "current_thread")]
    pub(super) async fn fetch_account_settings() -> ResultType<()> {
        if LocalConfig::get_option("access_token").is_empty() {
            return Ok(());
        }
        let api = crate::common::get_api_server(
            Config::get_option("api-server"),
            Config::get_option("custom-rendezvous-server"),
        );
        if api.is_empty() {
            return Ok(());
        }
        let body = serde_json::json!({
            "id": Config::get_id(),
            "uuid": crate::encode64(hbb_common::get_uuid()),
            "modified_at": 0,
        });
        let resp =
            crate::post_request(format!("{api}/api/heartbeat"), body.to_string(), "").await?;
        let resp: serde_json::Value = serde_json::from_str(&resp)?;
        if let Some(strategy) = resp.get("strategy") {
            let strategy: StrategyOptions = serde_json::from_value(strategy.clone())?;
            handle_config_options(strategy.config_options);
            log::info!("account settings applied");
        }
        Ok(())
    }
}

// Absolute input source selection for the soft keyboard's Hangul keys
// (flutter/lib/mobile/hangul_keys.dart). A plain legacy `Hangul` key toggles; with
// `press` set it selects: Korean when Shift is among the modifiers, English otherwise.
#[allow(dead_code)]
pub fn ime_mode_event(korean: bool) -> base::message_proto::KeyEvent {
    use base::message_proto::{ControlKey, KeyEvent, KeyboardMode};
    let mut evt = KeyEvent::new();
    evt.mode = KeyboardMode::Legacy.into();
    evt.down = true;
    evt.press = true;
    evt.set_control_key(ControlKey::Hangul);
    if korean {
        evt.modifiers.push(ControlKey::Shift.into());
    }
    evt
}

#[allow(dead_code)]
pub fn ime_mode_request(evt: &base::message_proto::KeyEvent) -> Option<bool> {
    use base::message_proto::{key_event, ControlKey, KeyboardMode};
    if !evt.press || evt.mode.enum_value() != Ok(KeyboardMode::Legacy) {
        return None;
    }
    match &evt.union {
        Some(key_event::Union::ControlKey(ck)) if ck.enum_value() == Ok(ControlKey::Hangul) => {
            Some(
                evt.modifiers
                    .iter()
                    .any(|m| m.enum_value() == Ok(ControlKey::Shift)),
            )
        }
        _ => None,
    }
}

/// Types the output of `HangulKeys.edit` as physical key presses (USB HID codes in map
/// mode), so the controlled side's own IME composes the text.
#[cfg(feature = "flutter")]
pub fn input_ime_keys<T: crate::ui_session_interface::InvokeUiSession>(
    session: &crate::ui_session_interface::Session<T>,
    keys: &str,
) {
    const HID_LEFT_SHIFT: i32 = 0xE1;
    let key = |hid: i32, down: bool| session.handle_flutter_key_event("map", "", hid, 0, down);
    let mut text = String::new();
    for c in keys.chars() {
        let hid = us_key(c);
        if !text.is_empty() && (hid.is_some() || c == '\u{1}' || c == '\u{2}') {
            session.input_string(&std::mem::take(&mut text));
        }
        match (c, hid) {
            ('\u{1}', _) | ('\u{2}', _) => session.send_key_event(&ime_mode_event(c == '\u{1}')),
            (_, Some((hid, shift))) => {
                if shift {
                    key(HID_LEFT_SHIFT, true);
                }
                key(hid, true);
                key(hid, false);
                if shift {
                    key(HID_LEFT_SHIFT, false);
                }
            }
            _ => text.push(c),
        }
    }
    if !text.is_empty() {
        session.input_string(&text);
    }
}

/// USB HID usage and Shift for a character on a US keyboard.
#[cfg(feature = "flutter")]
fn us_key(c: char) -> Option<(i32, bool)> {
    const ROW: &str = "1234567890";
    const SHIFTED_ROW: &str = "!@#$%^&*()";
    const PUNCT: &str = "-=[]\\;'`,./";
    const SHIFTED_PUNCT: &str = "_+{}|:\"~<>?";
    const PUNCT_HID: [i32; 11] = [
        0x2D, 0x2E, 0x2F, 0x30, 0x31, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38,
    ];
    Some(match c {
        'a'..='z' => (0x04 + (c as i32 - 'a' as i32), false),
        'A'..='Z' => (0x04 + (c as i32 - 'A' as i32), true),
        '\u{8}' => (0x2A, false),
        '\n' => (0x28, false),
        '\t' => (0x2B, false),
        ' ' => (0x2C, false),
        _ => {
            if let Some(i) = ROW.find(c) {
                (0x1E + i as i32, false)
            } else if let Some(i) = SHIFTED_ROW.find(c) {
                (0x1E + i as i32, true)
            } else if let Some(i) = PUNCT.find(c) {
                (PUNCT_HID[i], false)
            } else if let Some(i) = SHIFTED_PUNCT.find(c) {
                (PUNCT_HID[i], true)
            } else {
                return None;
            }
        }
    })
}
