#pragma once

// kpm-archive/docs/SSH.md section 7: the Force Wi-Fi row of the SSH tab. The
// setting is [DeveloperSettings] ForceWifiOn of Nickel. The hook changes it
// inside Nickel, so it applies without a reboot. A change of Kobo eReader.conf
// from outside Nickel does not apply: Nickel keeps its settings in memory and
// can write over the file.
//
// forcewifi.cc is built for the device only. The sim has its own version in
// sim/nickelstub.cc.

// nkpm_force_wifi returns 1 or 0, or -1 when this firmware does not have the
// symbols or the object layout is not the expected one.
int nkpm_force_wifi();

// nkpm_set_force_wifi sets the value. It returns false when the value is not
// available or does not read back as set.
bool nkpm_set_force_wifi(bool on);

#ifndef NKPM_SIM
// Resolved by the dlsym table of nkpm.cc. All are optional: a missing symbol
// hides the row.
extern void *(*Device__getCurrentDevice)();
extern void (*Settings__constructor)(void *_this, void *device, bool);
extern void (*Settings__destructor)(void *_this);
extern void *Settings__vtable;
extern void *DevSettings__vtable;
extern bool (*DevSettings__forceWifiOn)(void *_this);
extern void (*DevSettings__setForceWifiOn)(void *_this, bool);
#endif
