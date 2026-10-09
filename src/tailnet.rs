//! Tailnet-only build: every peer, server and relay is reached over Tailscale.
//!
//! The socket layer in `hbb_common::tailnet` refuses non-Tailscale addresses. This module turns
//! off the paths that bypass it (UDP/IPv6 punch, KCP, WebRTC, websocket, LAN discovery), keeps
//! direct IP access on, and filters incoming connections and HTTP requests.

use base::config::keys;
use hbb_common::{
    config::{DEFAULT_DISPLAY_SETTINGS, OVERWRITE_LOCAL_SETTINGS, OVERWRITE_SETTINGS},
    log, tailnet,
};
use std::net::{IpAddr, SocketAddr};

pub const ENFORCED: bool = true;

pub fn init() {
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
