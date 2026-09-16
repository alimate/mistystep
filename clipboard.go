package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
)

// GetClipboard reads the current clipboard content.
// Detection order: X11 (xclip, incl. via Xwayland) → Wayland → error.
//
// X11 is preferred because on compositors without the wlr/ext data-control
// protocol (GNOME/Mutter), wl-paste maps a transient window to gain keyboard
// focus on every call. Polled every second, that steals focus from the active
// app and flashes an icon in the dock. xclip reads through Xwayland's
// clipboard bridge with an unmapped window, so it never takes focus.
func GetClipboard() (string, error) {
	if os.Getenv("DISPLAY") != "" && hasXclip() {
		return x11_get()
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return wayland_get()
	}
	if os.Getenv("DISPLAY") != "" {
		return x11_get()
	}
	return "", errors.New("no display server detected (WAYLAND_DISPLAY and DISPLAY are unset)")
}

// SetClipboard writes text to the clipboard.
// Detection order: Wayland → X11 → error.
func SetClipboard(text string) error {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return wayland_set(text)
	}
	if os.Getenv("DISPLAY") != "" {
		return x11_set(text)
	}
	return errors.New("no display server detected (WAYLAND_DISPLAY and DISPLAY are unset)")
}

func hasXclip() bool {
	_, err := exec.LookPath("xclip")
	return err == nil
}

func wayland_get() (string, error) {
	cmd := exec.Command("wl-paste", "--no-newline")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// wl-paste exits 1 when clipboard is empty or contains non-text (e.g. image)
			return "", nil
		}
		return "", err
	}
	return string(out), nil
}

func wayland_set(text string) error {
	cmd := exec.Command("wl-copy")
	cmd.Stdin = bytes.NewBufferString(text)
	return cmd.Run()
}

func x11_get() (string, error) {
	cmd := exec.Command("xclip", "-selection", "clipboard", "-o")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return string(out), nil
}

func x11_set(text string) error {
	cmd := exec.Command("xclip", "-selection", "clipboard", "-i")
	cmd.Stdin = bytes.NewBufferString(text)
	return cmd.Run()
}
