# kpm ssh

kpm has a built-in SSH server for root logins (`kpm ssh`, kpm 0.14.0). It works on firmware 4.x and 6.x and replaces NiLuJe usbnet (Dropbear and telnet).

## Set it up

1. Run `kpm ssh install` once, or open the **SSH** tab of the package manager. kpm makes `.adds/kpm/ssh/`.
2. Connect the Kobo over USB. Add your public key to `.adds/authorized_keys`, one OpenSSH key per line (for example the line of `id_ed25519.pub`). Other Kobo SSH mods read this file too. `.adds/kpm/ssh/authorized_keys` also works.
3. Tap **Enable** on the SSH tab, or run `kpm ssh enable`. The server starts now and at each boot.
4. Log in with `ssh root@<device address>`. On the SSH tab, tap the eye icon to show the address. The tab hides it again each time it opens, so it is not on a screenshot. The tab also shows the host key fingerprint.

## Commands

```
kpm ssh install [--keys <file>] [--port N]   make .adds/kpm/ssh: host key, config.toml, authorized_keys
kpm ssh enable [--port N]                    install if needed, start at each boot, start now
kpm ssh disable                              do not start at boot, stop now
kpm ssh start|stop|restart                   control the server now ("enabled" does not change)
kpm ssh status [--json]                      enabled, running, pid, port, addresses, host key, keys
```

## How it works

- The server is the kpm binary itself (`kpm ssh serve`, in Go). It needs no other file on the device.
- The kpm hook starts it at boot when `.adds/kpm/ssh/config.toml` has `enabled = true`. It keeps running when Nickel restarts.
- It accepts the public keys of `.adds/authorized_keys` and `.adds/kpm/ssh/authorized_keys` only, for the user `root`. No password login. It reads both files again at each login, so a new key works at once. kpm never writes `.adds/authorized_keys`. `kpm ssh install --keys` writes to `.adds/kpm/ssh/authorized_keys`.
- A key line with options (`from=`, `command=` and others) is skipped and logged: kpm cannot apply the options.
- You get a shell with a PTY, commands without a PTY (`ssh kobo 'cmd'`), and `sftp`. OpenSSH `scp` uses sftp, so it works. The old scp protocol (`scp -O`) does not.
- Port forwarding, agent forwarding and X11 are refused.
- At most 8 connections. After 5 failed logins from one address in one minute, kpm refuses that address for 5 minutes.
- The log is `.adds/kpm/ssh/sshd.log`. `kpm ssh status` shows its last error.
- `config.toml` has `enabled`, `port` (default 22) and `listen` (default `0.0.0.0`). `kpm ssh enable --port 2222` changes the port.
- kpm ssh does not need any part of usbnet. The stock `/etc/fstab` mounts `/dev/pts`, and kpm ssh mounts it if it is missing.

## Force Wi-Fi

The SSH tab has a **Force Wi-Fi** row with a **Turn on** or **Turn off** button. Turn it on to keep Wi-Fi on while the Kobo is awake, so SSH stays reachable. Sleep still turns Wi-Fi off.

The button changes the Nickel developer setting `ForceWifiOn` at once, with no reboot. Nickel saves it in `Kobo eReader.conf`, so it stays after a reboot. `kpm ssh status` shows the value of that file. The kpm CLI does not change it. The row hides on a firmware that does not have the setting.

## Move from NiLuJe usbnet

NiLuJe Dropbear holds port 22, so `kpm ssh start` fails with "port 22 is in use" while it runs. Move from Dropbear to kpm ssh like this:

1. Run `kpm ssh enable --port 2222`. Log in on port 2222 and check that it works.
2. Stop Dropbear and telnet, and keep them off at boot. The KoboStuff NickelMenu toggle does this: it creates `NO_SSH` and `NO_TELNET` in `/usr/local/niluje/usbnet/etc`.
3. Run `kpm ssh enable --port 22`. `enable` restarts a running server, so it moves to port 22 at once.
4. Log in on port 22 in a new connection before you close the old one. The host key on port 22 is now the kpm key, so remove the old line from your `known_hosts` (`ssh-keygen -R <device address>`).

## Security

The user partition is FAT, so these files have no Unix permissions. A person with USB access to the Kobo can add a key to a key file. That person can already install a `KoboRoot.tgz` that runs as root, so kpm ssh gives them no new access.

## Uninstall

The UI uninstall flag (`kpm_ui_uninstall`) stops the server. The folder `.adds/kpm/ssh/` stays, with your keys and the host key, so a new install keeps the same host key. To remove kpm completely, run `kpm ssh disable` first.
