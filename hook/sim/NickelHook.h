#ifndef NH_H
#define NH_H

// Desktop-simulator stub for NickelHook.h (hook/sim, Tier-3 off-device testing).
//
// The real NickelHook.h (hook/NickelHook/NickelHook.h) is the device mod-loader
// ABI. The dialog/widget sources compiled into the sim only reference ONE symbol
// from it: nh_log (kpmprocess.cc, pagedstack.cc). This stub provides nh_log as an
// stderr printf and nothing else. It wins over the real header purely by
// include-path ordering: the sim build adds -I<hook/sim> and does NOT add
// -I<hook/NickelHook>, so `#include <NickelHook.h>` (an angle-include, which never
// searches the including file's own directory) resolves here. The device build is
// untouched.

#include <cstdarg>
#include <cstdio>

// nh_log mirrors the device logger's signature/format-checking, writing to stderr
// with a NickelKPM prefix so the sim's console shows the same log stream the mod
// would emit on-device.
static inline void nh_log(const char *fmt, ...) __attribute__((format(printf, 1, 2)));
static inline void nh_log(const char *fmt, ...) {
  va_list ap;
  va_start(ap, fmt);
  std::fputs("NickelKPM(sim): ", stderr);
  std::vfprintf(stderr, fmt, ap);
  std::fputc('\n', stderr);
  va_end(ap);
}

#endif
