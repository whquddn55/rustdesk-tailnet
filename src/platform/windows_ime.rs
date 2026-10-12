//! Korean IME state of the foreground window, for the soft keyboard's absolute
//! input source selection (`tailnet::ime_mode_request`).

use winapi::{
    shared::{minwindef::WPARAM, windef::HWND},
    um::winuser::{GetForegroundWindow, SendMessageTimeoutW, SMTO_ABORTIFHUNG},
};

#[link(name = "imm32")]
extern "system" {
    fn ImmGetDefaultIMEWnd(hwnd: HWND) -> HWND;
}

const WM_IME_CONTROL: u32 = 0x0283;
const IMC_GETCONVERSIONMODE: WPARAM = 0x0001;
const IME_CMODE_NATIVE: usize = 0x0001;

/// Whether the foreground window's IME is in Hangul mode; None when it has no IME
/// window or does not answer.
pub fn is_korean_mode() -> Option<bool> {
    unsafe {
        let foreground = GetForegroundWindow();
        if foreground.is_null() {
            return None;
        }
        let ime = ImmGetDefaultIMEWnd(foreground);
        if ime.is_null() {
            return None;
        }
        let mut mode: usize = 0;
        let ok = SendMessageTimeoutW(
            ime,
            WM_IME_CONTROL,
            IMC_GETCONVERSIONMODE,
            0,
            SMTO_ABORTIFHUNG,
            100,
            &mut mode,
        );
        if ok == 0 {
            return None;
        }
        Some(mode & IME_CMODE_NATIVE != 0)
    }
}
