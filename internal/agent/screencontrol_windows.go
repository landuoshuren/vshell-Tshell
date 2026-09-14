//go:build windows

package agent

import (
	"fmt"
	"strings"
	"syscall"
)

const (
	mouseLeftDown  = 0x0002
	mouseLeftUp    = 0x0004
	mouseRightDown = 0x0008
	mouseRightUp   = 0x0010
	mouseWheel     = 0x0800
	keyUp          = 0x0002
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	setCursorPosProc = user32.NewProc("SetCursorPos")
	getSystemMetrics = user32.NewProc("GetSystemMetrics")
	mouseEventProc   = user32.NewProc("mouse_event")
	keybdEventProc   = user32.NewProc("keybd_event")
	vkKeyScanWProc   = user32.NewProc("VkKeyScanW")
)

func number(p map[string]any, key string) float64 {
	switch v := p[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func screenControl(p map[string]any) error {
	switch str(p, "type") {
	case "1":
		canvasWidth, canvasHeight := number(p, "canvasWidth"), number(p, "canvasHeight")
		if canvasWidth <= 0 || canvasHeight <= 0 {
			return fmt.Errorf("invalid canvas size")
		}
		screenWidth, _, _ := getSystemMetrics.Call(0)
		screenHeight, _, _ := getSystemMetrics.Call(1)
		x := int32(number(p, "absX") * float64(screenWidth) / canvasWidth)
		y := int32(number(p, "absY") * float64(screenHeight) / canvasHeight)
		r1, _, err := setCursorPosProc.Call(uintptr(x), uintptr(y))
		if r1 == 0 {
			return err
		}
	case "2":
		mouseEventProc.Call(mouseLeftDown, 0, 0, 0, 0)
		mouseEventProc.Call(mouseLeftUp, 0, 0, 0, 0)
	case "4":
		mouseEventProc.Call(mouseRightDown, 0, 0, 0, 0)
		mouseEventProc.Call(mouseRightUp, 0, 0, 0, 0)
	case "5":
		mouseEventProc.Call(mouseLeftDown, 0, 0, 0, 0)
	case "6":
		mouseEventProc.Call(mouseLeftUp, 0, 0, 0, 0)
	case "7":
		amount := int32(number(p, "amount"))
		if strings.EqualFold(str(p, "direction"), "down") {
			amount = -amount
		}
		mouseEventProc.Call(mouseWheel, 0, 0, uintptr(uint32(amount)), 0)
	case "3":
		return sendKey(str(p, "keyCode"), nil)
	case "8":
		mods, _ := p["modifiers"].(map[string]any)
		return sendKey(str(p, "key"), mods)
	}
	return nil
}

func sendKey(key string, explicit map[string]any) error {
	vk, inferredMods := virtualKey(key)
	if vk == 0 {
		return fmt.Errorf("unsupported key %q", key)
	}
	mods := inferredMods
	if truthyMap(explicit, "shift") {
		mods |= 1
	}
	if truthyMap(explicit, "ctrl") {
		mods |= 2
	}
	if truthyMap(explicit, "alt") {
		mods |= 4
	}
	meta := truthyMap(explicit, "meta")
	if mods&2 != 0 {
		keyEvent(0x11, false)
	}
	if mods&4 != 0 {
		keyEvent(0x12, false)
	}
	if mods&1 != 0 {
		keyEvent(0x10, false)
	}
	if meta {
		keyEvent(0x5B, false)
	}
	keyEvent(vk, false)
	keyEvent(vk, true)
	if meta {
		keyEvent(0x5B, true)
	}
	if mods&1 != 0 {
		keyEvent(0x10, true)
	}
	if mods&4 != 0 {
		keyEvent(0x12, true)
	}
	if mods&2 != 0 {
		keyEvent(0x11, true)
	}
	return nil
}

func virtualKey(key string) (byte, byte) {
	named := map[string]byte{
		"Backspace": 0x08, "Tab": 0x09, "Enter": 0x0D, "Shift": 0x10,
		"Control": 0x11, "Alt": 0x12, "Escape": 0x1B, " ": 0x20,
		"PageUp": 0x21, "PageDown": 0x22, "End": 0x23, "Home": 0x24,
		"ArrowLeft": 0x25, "ArrowUp": 0x26, "ArrowRight": 0x27, "ArrowDown": 0x28,
		"Insert": 0x2D, "Delete": 0x2E,
	}
	for i := 1; i <= 12; i++ {
		named[fmt.Sprintf("F%d", i)] = byte(0x6F + i)
	}
	if vk := named[key]; vk != 0 {
		return vk, 0
	}
	runes := []rune(key)
	if len(runes) != 1 || runes[0] > 0xFFFF {
		return 0, 0
	}
	r1, _, _ := vkKeyScanWProc.Call(uintptr(runes[0]))
	if int16(r1) == -1 {
		return 0, 0
	}
	return byte(r1), byte(r1 >> 8)
}

func keyEvent(vk byte, up bool) {
	flags := uintptr(0)
	if up {
		flags = keyUp
	}
	keybdEventProc.Call(uintptr(vk), 0, flags, 0)
}

func truthyMap(p map[string]any, key string) bool {
	if p == nil {
		return false
	}
	v, _ := p[key].(bool)
	return v
}
