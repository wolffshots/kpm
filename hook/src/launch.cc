// Where kpm opens from. See launch.h.

#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

#include <string>

#include "launch.h"

namespace {

char const kOffPrefix[] = "#kpm-off ";
char const kHideText[] = "# kpm hides the NickelMenu button in the bottom bar (kpm UI, Settings tab).\n"
                         "# Delete this file and restart the device to show the button again.\n"
                         "experimental:menu_main_15505_enabled:0\n";
// The temporary files stay out of .adds/nm, where NickelMenu reads every file.
char const kTmp[] = "/mnt/onboard/.adds/kpm/.launch-tmp";

// readFile reads at most 64 KiB of path. It returns false when the file does
// not exist or cannot be read.
bool readFile(char const *path, std::string *out) {
  int fd = open(path, O_RDONLY | O_CLOEXEC);
  if (fd < 0) {
    return false;
  }
  char buf[4096];
  out->clear();
  for (;;) {
    ssize_t n = read(fd, buf, sizeof(buf));
    if (n <= 0) {
      break;
    }
    out->append(buf, (size_t)n);
    if (out->size() > 65536) {
      break;
    }
  }
  close(fd);
  return true;
}

// writeAtomic writes data to tmp, syncs it, and renames it to path.
int writeAtomic(char const *path, char const *tmp, std::string const &data) {
  int fd = open(tmp, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0644);
  if (fd < 0) {
    return -1;
  }
  bool ok = write(fd, data.data(), data.size()) == (ssize_t)data.size();
  ok = fsync(fd) == 0 && ok;
  close(fd);
  if (!ok || rename(tmp, path) != 0) {
    unlink(tmp);
    return -1;
  }
  return 0;
}

// tmpFor gives the temporary file of path: next to ui.toml for ui.toml, and
// kTmp for the files in .adds/nm. The test uses its own folders, so a path
// that is not under /mnt/onboard gets a temporary file next to it.
std::string tmpFor(char const *path) {
  if (strncmp(path, "/mnt/onboard/.adds/nm/", 22) == 0) {
    return kTmp;
  }
  return std::string(path) + ".tmp";
}

// valueOf returns the text after "key =" on a line of text, or "" when no line
// has the key.
std::string valueOf(std::string const &text, char const *key) {
  size_t const klen = strlen(key);
  size_t pos = 0;
  while (pos < text.size()) {
    size_t end = text.find('\n', pos);
    if (end == std::string::npos) {
      end = text.size();
    }
    std::string line = text.substr(pos, end - pos);
    pos = end + 1;
    size_t i = line.find_first_not_of(" \t");
    if (i == std::string::npos || line.compare(i, klen, key) != 0) {
      continue;
    }
    i = line.find_first_not_of(" \t", i + klen);
    if (i == std::string::npos || line[i] != '=') {
      continue;
    }
    size_t hash = line.find('#', i);
    return line.substr(i + 1, hash == std::string::npos ? std::string::npos : hash - i - 1);
  }
  return "";
}

// setEntries comments out (on 0) or restores (on 1) each line of a NickelMenu
// config file.
std::string setEntries(std::string const &text, int on) {
  size_t const plen = sizeof(kOffPrefix) - 1;
  std::string out;
  size_t pos = 0;
  while (pos < text.size()) {
    size_t end = text.find('\n', pos);
    bool const nl = end != std::string::npos;
    std::string line = text.substr(pos, nl ? end - pos : std::string::npos);
    pos = nl ? end + 1 : text.size();
    bool const off = line.compare(0, plen, kOffPrefix) == 0;
    if (on && off) {
      line.erase(0, plen);
    } else if (!on && !off) {
      size_t i = line.find_first_not_of(" \t\r");
      if (i != std::string::npos && line[i] != '#') {
        line.insert(0, kOffPrefix);
      }
    }
    out += line;
    if (nl) {
      out += '\n';
    }
  }
  return out;
}

} // namespace

void nkpm_launch_read(char const *path, struct nkpm_launch *out) {
  out->more_row = 1;
  out->hide_nm_button = 0;
  std::string text;
  if (!readFile(path, &text)) {
    return;
  }
  std::string from = valueOf(text, "open_from");
  if (from.find("nickelmenu") != std::string::npos) {
    out->more_row = 0;
  }
  std::string hide = valueOf(text, "hide_nickelmenu_button");
  if (hide.find("true") != std::string::npos) {
    out->hide_nm_button = 1;
  }
}

int nkpm_launch_write(char const *path, struct nkpm_launch const *s) {
  std::string text = "# kpm UI settings. The Settings tab of the kpm UI writes this file.\n"
                     "# open_from: \"more\" (a row on the More page) or \"nickelmenu\".\n";
  text += s->more_row ? "open_from = \"more\"\n" : "open_from = \"nickelmenu\"\n";
  text += s->hide_nm_button ? "hide_nickelmenu_button = true\n" : "hide_nickelmenu_button = false\n";
  return writeAtomic(path, tmpFor(path).c_str(), text);
}

struct nkpm_launch_state nkpm_launch_decide(struct nkpm_launch const *s, int nm_installed, int row_available) {
  struct nkpm_launch_state st;
  st.use_row = row_available && (s->more_row || !nm_installed);
  st.entries = !st.use_row;
  st.hide = st.use_row && s->more_row && s->hide_nm_button;
  return st;
}

int nkpm_launch_apply(char const *nm_file, char const *hide_file, int entries, int hide) {
  int rc = 0;
  std::string text;
  if (readFile(nm_file, &text)) {
    std::string want = setEntries(text, entries);
    if (want != text && writeAtomic(nm_file, tmpFor(nm_file).c_str(), want) != 0) {
      rc = -1;
    }
  }
  if (hide) {
    std::string have;
    if ((!readFile(hide_file, &have) || have != kHideText) &&
        writeAtomic(hide_file, tmpFor(hide_file).c_str(), kHideText) != 0) {
      rc = -1;
    }
  } else if (unlink(hide_file) != 0 && access(hide_file, F_OK) == 0) {
    rc = -1;
  }
  return rc;
}

int nkpm_nm_installed(char const *dir) {
  std::string const d(dir);
  std::string const up = d.substr(0, d.rfind('/'));
  for (char const *name : {"libnm.so", "libnm-qt6.so"}) {
    std::string const n(name);
    for (std::string const &p : {d + "/" + n, d + "/" + n + ".failsafe", up + "/" + n + ".failsafe"}) {
      if (access(p.c_str(), F_OK) == 0) {
        return 1;
      }
    }
  }
  return 0;
}
