#pragma once

// kpm-nickelmenu-less-research-2026-11-24.md section 8: a "Package manager" row
// on the Nickel More page, directly above Help. See morerow.cc.

// nkpm_morerow_init installs the application event filter that inserts the row.
// A tap on the row calls open. It does nothing when the crash guard blocks the
// row (moreguard.h).
void nkpm_morerow_init(void (*open)());
