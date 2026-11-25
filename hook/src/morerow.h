#pragma once

// kpm-nickelmenu-less-research-2026-11-24.md section 8: a "Package manager" row
// on the Nickel More page, directly above Help. See morerow.cc.

// nkpm_morerow_init installs the application event filter that inserts the row.
// A tap on the row calls open. It does nothing when the crash guard blocks the
// row (moreguard.h). It returns 1 when the filter is installed, else 0. The row
// shows only while the settings choose it (launch.h).
int nkpm_morerow_init(void (*open)());

// nkpm_morerow_available returns 1 while the row can show this boot: the filter
// is installed and no More page lacked its Help row.
int nkpm_morerow_available();
