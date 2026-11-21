// Host test of src/ssh.cc (kpm-archive/docs/SSH.md section 5 and 8). Build and run:
//   g++ -std=gnu++11 -Wall -Wextra -I../src -o /tmp/ssh_test ssh_test.cc ../src/ssh.cc && /tmp/ssh_test

#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/wait.h>
#include <unistd.h>

#include <string>

#include "ssh.h"

static int failures = 0;

// LIT is a string literal with its NUL bytes, without the final NUL of C.
#define LIT(s) std::string(s, sizeof(s) - 1)

#define CHECK(cond)                                                                                                    \
  do {                                                                                                                 \
    if (!(cond)) {                                                                                                     \
      fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);                                                  \
      failures++;                                                                                                      \
    }                                                                                                                  \
  } while (0)

static void put(std::string const &path, std::string const &data) {
  std::string dir = path.substr(0, path.rfind('/'));
  if (system(("mkdir -p '" + dir + "'").c_str()) != 0) {
    abort();
  }
  FILE *f = fopen(path.c_str(), "w");
  fwrite(data.data(), 1, data.size(), f);
  fclose(f);
}

static pid_t sleeper() {
  pid_t pid = fork();
  if (pid == 0) {
    pause();
    _exit(0);
  }
  return pid;
}

// reaped returns the signal that ended pid, or 0 when it still runs.
static int reaped(pid_t pid) {
  int status = 0;
  for (int i = 0; i < 100; i++) {
    if (waitpid(pid, &status, WNOHANG) == pid) {
      return WIFSIGNALED(status) ? WTERMSIG(status) : -1;
    }
    usleep(10000);
  }
  return 0;
}

int main() {
  char tmpl[] = "/tmp/nkpm-ssh-XXXXXX";
  std::string dir = mkdtemp(tmpl);
  std::string cfg = dir + "/config.toml";
  std::string pidf = dir + "/sshd.pid";
  std::string proc = dir + "/proc";

  // nkpm_ssh_enabled
  CHECK(nkpm_ssh_enabled(cfg.c_str()) == 0); // missing file
  put(cfg, "# kpm ssh settings.\nenabled = true\nport = 22\nlisten = \"0.0.0.0\"\n");
  CHECK(nkpm_ssh_enabled(cfg.c_str()) == 1);
  put(cfg, "enabled = false\nport = 22\n");
  CHECK(nkpm_ssh_enabled(cfg.c_str()) == 0);
  put(cfg, "  enabled=true   # on\r\n");
  CHECK(nkpm_ssh_enabled(cfg.c_str()) == 1);
  put(cfg, "# enabled = true\nenabled_x = true\n");
  CHECK(nkpm_ssh_enabled(cfg.c_str()) == 0);
  put(cfg, "enabled = \"true\"\n");
  CHECK(nkpm_ssh_enabled(cfg.c_str()) == 0);

  // nkpm_ssh_stop: no pid file, a bad pid, a pid of another program
  CHECK(nkpm_ssh_stop(pidf.c_str(), proc.c_str()) == 0);
  put(pidf, "nope\n");
  CHECK(nkpm_ssh_stop(pidf.c_str(), proc.c_str()) == 0);

  pid_t other = sleeper();
  put(pidf, std::to_string(other) + "\n");
  put(proc + "/" + std::to_string(other) + "/cmdline", LIT("/bin/sleep\0" "30\0"));
  CHECK(nkpm_ssh_stop(pidf.c_str(), proc.c_str()) == 0);
  put(proc + "/" + std::to_string(other) + "/cmdline", LIT("/mnt/onboard/.adds/kpm/bin/kpm\0ssh\0status\0"));
  CHECK(nkpm_ssh_stop(pidf.c_str(), proc.c_str()) == 0);
  put(proc + "/" + std::to_string(other) + "/cmdline", LIT("/usr/bin/notkpm\0ssh\0serve\0"));
  CHECK(nkpm_ssh_stop(pidf.c_str(), proc.c_str()) == 0);
  CHECK(reaped(other) == 0); // not signaled
  kill(other, SIGKILL);
  waitpid(other, nullptr, 0);

  // the server: signaled with SIGTERM
  pid_t server = sleeper();
  put(pidf, std::to_string(server) + "\n");
  put(proc + "/" + std::to_string(server) + "/cmdline", LIT("/mnt/onboard/.adds/kpm/bin/kpm\0ssh\0serve\0"));
  CHECK(nkpm_ssh_stop(pidf.c_str(), proc.c_str()) == 1);
  CHECK(reaped(server) == SIGTERM);

  // the server under qemu-user (kfw vm): a launcher comes first
  server = sleeper();
  put(pidf, std::to_string(server) + "\n");
  put(proc + "/" + std::to_string(server) + "/cmdline",
      LIT("/usr/bin/qemu-arm-static\0/mnt/onboard/.adds/kpm/bin/kpm\0ssh\0serve\0"));
  CHECK(nkpm_ssh_stop(pidf.c_str(), proc.c_str()) == 1);
  CHECK(reaped(server) == SIGTERM);

  if (system(("rm -rf '" + dir + "'").c_str()) != 0) {
    return 2;
  }
  if (failures > 0) {
    fprintf(stderr, "%d failure(s)\n", failures);
    return 1;
  }
  printf("ssh_test: PASS\n");
  return 0;
}
