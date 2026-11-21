// kpm-archive/docs/SSH.md section 7: Force Wi-Fi. See forcewifi.h.
//
// libnickel exports no DevSettings constructor (4.46.23852 and 6.0.279225). So
// this file does what the NickelMenu action `nickel_setting force_wifi` does
// (src/action_cc.cc): construct a Settings object, then point its vtable at the
// DevSettings vtable, so the virtual section name is "DeveloperSettings". The
// cast is safe only when the Settings constructor set the expected vtable, so
// the code checks that first.

#include <NickelHook.h>

#include <cstdint>
#include <cstring>

#include "forcewifi.h"

void *(*Device__getCurrentDevice)();
void (*Settings__constructor)(void *_this, void *device, bool);
void (*Settings__destructor)(void *_this);
void *Settings__vtable;
void *DevSettings__vtable;
bool (*DevSettings__forceWifiOn)(void *_this);
void (*DevSettings__setForceWifiOn)(void *_this, bool);

// The first word of the object is the vtable pointer. It points 8 bytes into
// the vtable symbol (offset to top and type info come first on 32-bit ARM).
static void *&vtablePtr(void *obj) { return *reinterpret_cast<void **>(obj); }
static void *vtableTarget(void *vtable) { return reinterpret_cast<void *>(reinterpret_cast<uintptr_t>(vtable) + 8); }

static bool available() {
  return Device__getCurrentDevice && Settings__constructor && Settings__destructor && Settings__vtable &&
         DevSettings__vtable && DevSettings__forceWifiOn && DevSettings__setForceWifiOn;
}

// DevSettingsObject constructs the Settings object in buf and casts it to
// DevSettings. ok is false when the layout is not the expected one.
struct DevSettingsObject {
  // Larger than Settings on both firmware lines. NickelMenu uses 128 bytes.
  alignas(8) unsigned char buf[256];
  bool ok = false;

  DevSettingsObject() {
    memset(buf, 0, sizeof(buf));
    void *dev = Device__getCurrentDevice();
    if (dev == nullptr) {
      nh_log("force wifi: no current device");
      return;
    }
    Settings__constructor(buf, dev, false);
    if (vtablePtr(buf) != vtableTarget(Settings__vtable)) {
      nh_log("force wifi: unexpected Settings vtable, the row stays hidden");
      Settings__destructor(buf);
      return;
    }
    ok = true;
    cast();
  }

  ~DevSettingsObject() {
    if (ok) {
      vtablePtr(buf) = vtableTarget(Settings__vtable);
      Settings__destructor(buf);
    }
  }

  // cast sets the DevSettings vtable again: a call can change the pointer.
  void cast() { vtablePtr(buf) = vtableTarget(DevSettings__vtable); }
};

int nkpm_force_wifi() {
  if (!available()) {
    return -1;
  }
  DevSettingsObject s;
  if (!s.ok) {
    return -1;
  }
  bool on = DevSettings__forceWifiOn(s.buf);
  s.cast();
  return on ? 1 : 0;
}

bool nkpm_set_force_wifi(bool on) {
  if (!available()) {
    return false;
  }
  DevSettingsObject s;
  if (!s.ok) {
    return false;
  }
  DevSettings__setForceWifiOn(s.buf, on);
  s.cast();
  bool now = DevSettings__forceWifiOn(s.buf);
  s.cast();
  nh_log("force wifi: set %d, reads %d", on, now);
  return now == on;
}
