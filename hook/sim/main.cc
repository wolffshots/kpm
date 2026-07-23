// hook/sim — desktop simulator entry point.
//
// Wires the sim Nickel shim (nickelstub) into the REAL dialog sources and opens
// BrowseDialog, so the on-device UI flows can be exercised on a PC against a
// host-built kpm binary. Flags:
//   --size WxH               main-window size (default 758x1024, Kobo portrait)
//   --screenshot <dir>       offscreen: grab browse/detail/config png's, then exit
//   --exercise-uninstall <id>  offscreen: drive the Uninstall flow end-to-end
//   --exercise-config <id>     offscreen: drive Settings → edit → Save end-to-end
//   --exercise-init            offscreen: drive Settings → Open the un-created PIN
//                              template → "Create from example" → assert the file
//                              equals the template bytes, then edit one line
//   --exercise-sync            offscreen: tap Sync, assert samplemod's def gains
//                              the registry's config table (has_config flips true)
//   --exercise-enroll          offscreen: open kpm detail, tap "Enable self-update",
//                              reject the "Check now?" prompt, assert state.json gained
//                              packages.kpm.source and search reports self_configured
//   --exercise-badges          offscreen: assert the "files missing" browse badge
//                              (samplemod) and the "Untested on your firmware"
//                              detail warning (nickelnote) render (A + D)
//   --exercise-wake            offscreen: open browse → detail, assert the fake
//                              status bar + MainNavView are hidden, simulate a
//                              sleep/wake re-show, assert the ChromeGuard re-hid
//                              them, then close and assert both are restored
//
// Screenshot/exercise modes are designed for QT_QPA_PLATFORM=offscreen.

#include <cstdio>
#include <cstdlib>
#include <functional>

#include <QApplication>
#include <QDir>
#include <QFile>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QLabel>
#include <QLineEdit>
#include <QMouseEvent>
#include <QPixmap>
#include <QProcess>
#include <QString>
#include <QStringList>
#include <QTimer>

#include "browsedialog.h"
#include "configdialog.h"
#include "nickelstub.h"
#include "widgets/configrow.h"
#include "widgets/packagerow.h"
#include "widgets/pagedstack.h"

// sendClick synthesizes a left press+release at a widget's center, driving the
// same mouse path a tap would (PackageRow::selected / SimTouchLabel::tapped).
static void sendClick(QWidget *w) {
  QPoint c = w->rect().center();
  QPoint g = w->mapToGlobal(c);
  QMouseEvent press(QEvent::MouseButtonPress, c, g, Qt::LeftButton, Qt::LeftButton, Qt::NoModifier);
  QApplication::sendEvent(w, &press);
  QMouseEvent release(QEvent::MouseButtonRelease, c, g, Qt::LeftButton, Qt::LeftButton, Qt::NoModifier);
  QApplication::sendEvent(w, &release);
}

// waitForRows polls until the browse list has rendered PackageRows (the async
// `kpm search` has returned and laid out), then calls onReady; gives up after
// ~20s via onTimeout.
static void waitForRows(std::function<void()> onReady, std::function<void()> onTimeout) {
  QTimer *timer = new QTimer(qApp);
  int *elapsed = new int(0);
  QObject::connect(timer, &QTimer::timeout, qApp, [=]() {
    *elapsed += 200;
    QList<PackageRow *> rows = nickelstub_mainWindow()->findChildren<PackageRow *>();
    if (!rows.isEmpty()) {
      timer->stop();
      onReady();
    } else if (*elapsed > 20000) {
      timer->stop();
      onTimeout();
    }
  });
  timer->start(200);
}

static void saveGrab(const QString &path) {
  QPixmap pm = nickelstub_mainWindow()->grab();
  if (!pm.save(path)) {
    std::fprintf(stderr, "sim: failed to save %s\n", qPrintable(path));
  } else {
    std::fprintf(stderr, "sim: wrote %s (%dx%d)\n", qPrintable(path), pm.width(), pm.height());
  }
}

// findLabelByText returns the first tappable label with exactly this text (button
// labels are QLabel subclasses in both device and sim — see nickelstub.h).
static QLabel *findLabelByText(const QString &text) {
  for (QLabel *l : nickelstub_mainWindow()->findChildren<QLabel *>()) {
    if (l->text() == text) {
      return l;
    }
  }
  return nullptr;
}

static void runScreenshot(const QString &dir) {
  QDir().mkpath(dir);
  waitForRows(
      [dir]() {
        saveGrab(dir + "/browse.png"); // shows samplemod's "files missing" badge (A)
        QList<PackageRow *> rows = nickelstub_mainWindow()->findChildren<PackageRow *>();
        sendClick(rows.first()); // opens DetailDialog for the first package
        QTimer::singleShot(700, qApp, [dir]() {
          saveGrab(dir + "/detail.png");
          // D: nickelnote's registry def carries an older tested_fw than the seeded
          // device firmware, so its detail page shows the "Untested on your
          // firmware" warning. Open it over the current detail and grab that shot.
          if (BrowseDialog *browse = nickelstub_mainWindow()->findChild<BrowseDialog *>()) {
            QMetaObject::invokeMethod(browse, "openDetail", Q_ARG(QString, QStringLiteral("nickelnote")));
          }
          QTimer::singleShot(700, qApp, [dir]() {
            saveGrab(dir + "/detail-fw-untested.png"); // the firmware warning line
            ConfigDialog::show("nickelclock", "NickelClock");
            QTimer::singleShot(1800, qApp, [dir]() {
            saveGrab(dir + "/config-list.png"); // ini entries (Section · Key rows)
            if (QLabel *edit = findLabelByText("Edit")) {
              sendClick(edit); // raise the single-line editor for that entry
            }
            QTimer::singleShot(700, qApp, [dir]() {
              saveGrab(dir + "/config-edit.png"); // editor pre-filled with the value
              ConfigDialog::show("nickelnote", "NickelNote");
              QTimer::singleShot(1800, qApp, [dir]() {
                saveGrab(dir + "/config-files.png"); // multi-file picker
                // Open the not-created PIN template to capture the "Create from
                // example" affordance (CONFIG.md §3.x).
                QLabel *open = nullptr;
                for (ConfigFileRow *r : nickelstub_mainWindow()->findChildren<ConfigFileRow *>()) {
                  if (r->fileData().value("name").toString() == QStringLiteral("PIN screen message")) {
                    for (QLabel *l : r->findChildren<QLabel *>()) {
                      if (l->text() == "Open") {
                        open = l;
                      }
                    }
                  }
                }
                if (open) {
                  sendClick(open);
                }
                QTimer::singleShot(1500, qApp, [dir]() {
                  saveGrab(dir + "/config-init.png"); // the "Create from example" button
                  // Self-enrol: open the un-adopted kpm detail (fixture/seed.go) to
                  // capture the advisory line + the "Enable self-update" button.
                  if (BrowseDialog *browse = nickelstub_mainWindow()->findChild<BrowseDialog *>()) {
                    QMetaObject::invokeMethod(browse, "openDetail", Q_ARG(QString, QStringLiteral("kpm")));
                  }
                  QTimer::singleShot(700, qApp, [dir]() {
                    saveGrab(dir + "/detail-enroll.png"); // advisory + Enable self-update
                    qApp->quit();
                  });
                });
              });
            });
          });
          }); // close the detail-fw-untested timer
        });   // close the detail.png timer
      },
      [dir]() {
        std::fprintf(stderr, "sim: timed out waiting for packages\n");
        saveGrab(dir + "/browse.png");
        qApp->exit(3);
      });
}

static void runExerciseUninstall(const QString &id) {
  waitForRows(
      [id]() {
        BrowseDialog *browse = nickelstub_mainWindow()->findChild<BrowseDialog *>();
        if (!browse) {
          std::fprintf(stderr, "sim: no BrowseDialog found\n");
          qApp->exit(4);
          return;
        }
        // openDetail is a public slot on BrowseDialog (the real per-package view).
        QMetaObject::invokeMethod(browse, "openDetail", Q_ARG(QString, id));
        QTimer::singleShot(500, qApp, [id]() {
          // Find the Uninstall button (an N3ButtonLabel / SimTouchLabel).
          QLabel *btn = nullptr;
          for (QLabel *l : nickelstub_mainWindow()->findChildren<QLabel *>()) {
            if (l->text() == "Uninstall") {
              btn = l;
            }
          }
          if (!btn) {
            std::fprintf(stderr, "sim: no Uninstall button for %s\n", qPrintable(id));
            qApp->exit(5);
            return;
          }
          sendClick(btn); // -> DetailDialog::uninstall() -> confirmation dialog
          QTimer::singleShot(500, qApp, []() {
            if (!g_lastConfirmation) {
              std::fprintf(stderr, "sim: no confirmation dialog appeared\n");
              qApp->exit(6);
              return;
            }
            std::fprintf(stderr, "sim: accepting uninstall confirmation\n");
            g_lastConfirmation->accept(); // -> KpmProcess::uninstall runs kpm
            // Give the kpm process time to run and mutate the sandbox state.
            QTimer::singleShot(5000, qApp, []() { qApp->quit(); });
          });
        });
      },
      [id]() {
        std::fprintf(stderr, "sim: timed out waiting for packages\n");
        qApp->exit(3);
      });
}

// readBytes slurps a whole file (empty QByteArray if absent).
static QByteArray readBytes(const QString &path) {
  QFile f(path);
  if (!f.open(QIODevice::ReadOnly)) {
    return QByteArray();
  }
  return f.readAll();
}

// runExerciseConfig drives the full config-editing flow end to end, offline:
// open detail → Settings → ConfigDialog → tap an entry's Edit → clear + type a
// new value → Save (commit → `kpm config set`). It then byte-compares the
// on-disk settings.ini before/after and asserts the edit was SURGICAL — exactly
// one line changed, every other byte preserved (CONFIG.md §3 round-trip rule).
// Written for nickelclock (single ini file); it edits the [Clock] Enabled key.
static void runExerciseConfig(const QString &id) {
  QString sysroot = QString::fromLocal8Bit(qgetenv("KPM_SYSROOT"));
  QString host = sysroot + "/mnt/onboard/.adds/" + id + "/settings.ini";
  QByteArray *before = new QByteArray(readBytes(host));

  waitForRows(
      [id, host, before]() {
        BrowseDialog *browse = nickelstub_mainWindow()->findChild<BrowseDialog *>();
        if (!browse) {
          std::fprintf(stderr, "sim: no BrowseDialog found\n");
          qApp->exit(4);
          return;
        }
        QMetaObject::invokeMethod(browse, "openDetail", Q_ARG(QString, id));
        QTimer::singleShot(500, qApp, [id, host, before]() {
          QLabel *settings = findLabelByText("Settings");
          if (!settings) {
            std::fprintf(stderr, "sim: no Settings button for %s\n", qPrintable(id));
            qApp->exit(5);
            return;
          }
          sendClick(settings); // -> DetailDialog::openConfig() -> ConfigDialog (config list/show)
          QTimer::singleShot(1500, qApp, [id, host, before]() {
            // Locate the [Clock] Enabled entry row and tap its Edit button.
            ConfigEntryRow *target = nullptr;
            for (ConfigEntryRow *r : nickelstub_mainWindow()->findChildren<ConfigEntryRow *>()) {
              const QJsonObject &e = r->entryData();
              if (e.value("section").toString() == "Clock" && e.value("key").toString() == "Enabled") {
                target = r;
              }
            }
            if (!target) {
              std::fprintf(stderr, "sim: could not find the [Clock] Enabled row\n");
              qApp->exit(6);
              return;
            }
            QString oldValue = target->entryData().value("value").toString();
            QString newValue = oldValue == "true" ? "false" : "true";
            QLabel *edit = nullptr;
            for (QLabel *l : target->findChildren<QLabel *>()) {
              if (l->text() == "Edit") {
                edit = l;
              }
            }
            sendClick(edit); // -> ConfigEntryRow::selected -> ConfigDialog::beginEdit (editor shown)
            QTimer::singleShot(500, qApp, [id, host, before, newValue]() {
              ConfigDialog *cfg = nickelstub_mainWindow()->findChild<ConfigDialog *>();
              QLineEdit *field = cfg ? cfg->findChild<QLineEdit *>() : nullptr;
              if (!field) {
                std::fprintf(stderr, "sim: no edit field open\n");
                qApp->exit(7);
                return;
              }
              field->setText(newValue); // clear + type the new value
              std::fprintf(stderr, "sim: setting Clock/Enabled = %s\n", qPrintable(newValue));
              QMetaObject::invokeMethod(cfg, "commit"); // Save -> kpm config set
              // Give kpm time to write, then byte-compare the file.
              QTimer::singleShot(2500, qApp, [host, before, newValue]() {
                QByteArray after = readBytes(host);
                QList<QByteArray> a = before->split('\n');
                QList<QByteArray> b = after.split('\n');
                int rc = 0;
                if (a.size() != b.size()) {
                  std::fprintf(stderr, "sim: FAIL — line count changed (%d -> %d), edit was not surgical\n",
                               a.size(), b.size());
                  rc = 8;
                } else {
                  int diffs = 0, at = -1;
                  for (int i = 0; i < a.size(); i++) {
                    if (a.at(i) != b.at(i)) {
                      diffs++;
                      at = i;
                    }
                  }
                  if (diffs == 1) {
                    std::fprintf(stderr, "sim: PASS — 1 line changed (line %d): %s -> %s\n", at + 1,
                                 a.at(at).constData(), b.at(at).constData());
                    if (!b.at(at).contains(newValue.toUtf8())) {
                      std::fprintf(stderr, "sim: FAIL — changed line does not carry %s\n", qPrintable(newValue));
                      rc = 9;
                    }
                  } else {
                    std::fprintf(stderr, "sim: FAIL — %d lines changed, expected exactly 1\n", diffs);
                    rc = 8;
                  }
                }
                delete before;
                qApp->exit(rc);
              });
            });
          });
        });
      },
      [id, before]() {
        std::fprintf(stderr, "sim: timed out waiting for packages\n");
        delete before;
        qApp->exit(3);
      });
}

// expectedPinTemplate is the normalized bytes `kpm config init` must write for
// nickelnote's PIN template (fixture/seed.go's notePinTemplate, which already has
// a single trailing newline and no CR — so SeedContent reproduces it verbatim).
static QByteArray expectedPinTemplate() {
  return QByteArray("<p style=\"font-size: 32px;\">\n"
                    "This tablet is protected and belongs to <br/>\n"
                    "<b>Your Name</b>\n"
                    "<br/>\n"
                    "<br/>\n"
                    "If found, please return to <br/>\n"
                    "US: +1 555 000 0000<br/>\n"
                    "CA: +1 555 000 0000<br/>\n"
                    "</p>\n");
}

// runExerciseInit drives the "Create from example" flow end to end, offline:
// open detail (nickelnote) → Settings → the multi-file picker → Open the
// not-created PIN template → tap "Create from example" (→ `kpm config init`,
// which seeds the file from its template) → assert the on-disk file equals the
// normalized template bytes EXACTLY → then edit one line and Save, asserting the
// edit was surgical (exactly one line changed). Mirrors the seed's init target
// (fixture/seed.go: pin.template is the only nickelnote config left un-created).
static void runExerciseInit() {
  const QString id = QStringLiteral("nickelnote");
  const QString fileName = QStringLiteral("PIN screen message");
  QString sysroot = QString::fromLocal8Bit(qgetenv("KPM_SYSROOT"));
  QString host = sysroot + "/mnt/onboard/.adds/nickelnote/pin.template";

  waitForRows(
      [id, fileName, host]() {
        BrowseDialog *browse = nickelstub_mainWindow()->findChild<BrowseDialog *>();
        if (!browse) {
          std::fprintf(stderr, "sim: no BrowseDialog found\n");
          qApp->exit(4);
          return;
        }
        QMetaObject::invokeMethod(browse, "openDetail", Q_ARG(QString, id));
        QTimer::singleShot(500, qApp, [fileName, host]() {
          QLabel *settings = findLabelByText("Settings");
          if (!settings) {
            std::fprintf(stderr, "sim: no Settings button for nickelnote\n");
            qApp->exit(5);
            return;
          }
          sendClick(settings); // -> DetailDialog::openConfig() -> ConfigDialog (multi-file picker)
          QTimer::singleShot(1500, qApp, [fileName, host]() {
            // Locate the PIN template file row and tap its Open button.
            QLabel *open = nullptr;
            for (ConfigFileRow *r : nickelstub_mainWindow()->findChildren<ConfigFileRow *>()) {
              if (r->fileData().value("name").toString() == fileName) {
                for (QLabel *l : r->findChildren<QLabel *>()) {
                  if (l->text() == "Open") {
                    open = l;
                  }
                }
              }
            }
            if (!open) {
              std::fprintf(stderr, "sim: could not find the PIN template file row\n");
              qApp->exit(6);
              return;
            }
            sendClick(open); // -> ConfigDialog::openFile -> config show (exists:false, has_template:true)
            QTimer::singleShot(1500, qApp, [host]() {
              QLabel *create = findLabelByText("Create from example");
              if (!create) {
                std::fprintf(stderr, "sim: no 'Create from example' button for the un-created PIN template\n");
                qApp->exit(7);
                return;
              }
              std::fprintf(stderr, "sim: tapping 'Create from example'\n");
              sendClick(create); // -> ConfigDialog::createFromTemplate -> KpmProcess::configInit
              QTimer::singleShot(2500, qApp, [host]() {
                QByteArray got = readBytes(host);
                QByteArray want = expectedPinTemplate();
                if (got != want) {
                  std::fprintf(stderr, "sim: FAIL — seeded file does not match the template bytes\n got: %s\nwant: %s\n",
                               got.constData(), want.constData());
                  qApp->exit(8);
                  return;
                }
                std::fprintf(stderr, "sim: PASS — pin.template seeded byte-for-byte from the template (%d bytes)\n",
                             got.size());

                // Now edit exactly one line and Save; assert the edit is surgical.
                QByteArray *before = new QByteArray(got);
                ConfigEntryRow *target = nullptr;
                for (ConfigEntryRow *r : nickelstub_mainWindow()->findChildren<ConfigEntryRow *>()) {
                  target = r; // the last-laid-out row is fine; any single line works
                  break;
                }
                if (!target) {
                  std::fprintf(stderr, "sim: FAIL — no editable line after seeding\n");
                  delete before;
                  qApp->exit(9);
                  return;
                }
                int line = target->entryData().value("line").toInt();
                QLabel *edit = nullptr;
                for (QLabel *l : target->findChildren<QLabel *>()) {
                  if (l->text() == "Edit") {
                    edit = l;
                  }
                }
                sendClick(edit); // -> beginEdit (editor shown)
                QTimer::singleShot(500, qApp, [host, before, line]() {
                  ConfigDialog *cfg = nickelstub_mainWindow()->findChild<ConfigDialog *>();
                  QLineEdit *field = cfg ? cfg->findChild<QLineEdit *>() : nullptr;
                  if (!field) {
                    std::fprintf(stderr, "sim: FAIL — no edit field open after seeding\n");
                    delete before;
                    qApp->exit(10);
                    return;
                  }
                  field->setText(QStringLiteral("<p>edited line</p>"));
                  std::fprintf(stderr, "sim: editing seeded line %d\n", line);
                  QMetaObject::invokeMethod(cfg, "commit"); // Save -> kpm config set
                  QTimer::singleShot(2500, qApp, [host, before]() {
                    QByteArray after = readBytes(host);
                    QList<QByteArray> a = before->split('\n');
                    QList<QByteArray> b = after.split('\n');
                    int rc = 0;
                    if (a.size() != b.size()) {
                      std::fprintf(stderr, "sim: FAIL — line count changed (%d -> %d) after edit\n", a.size(), b.size());
                      rc = 11;
                    } else {
                      int diffs = 0, at = -1;
                      for (int i = 0; i < a.size(); i++) {
                        if (a.at(i) != b.at(i)) {
                          diffs++;
                          at = i;
                        }
                      }
                      if (diffs == 1) {
                        std::fprintf(stderr, "sim: PASS — post-seed edit changed exactly 1 line (line %d)\n", at + 1);
                      } else {
                        std::fprintf(stderr, "sim: FAIL — %d lines changed after edit, expected exactly 1\n", diffs);
                        rc = 11;
                      }
                    }
                    delete before;
                    qApp->exit(rc);
                  });
                });
              });
            });
          });
        });
      },
      []() {
        std::fprintf(stderr, "sim: timed out waiting for packages\n");
        qApp->exit(3);
      });
}

// runExerciseSync drives the Sync footer button end to end, offline: tap Sync ->
// BrowseDialog::sync() -> KpmProcess::sync() runs `kpm sync --json`, which re-copies
// the registry def (now carrying a config declaration the local def predated) into
// packages.d. It then asserts, from the sandbox, that (a) samplemod's packages.d
// def gained the [[configs]] table and (b) a fresh `kpm search --json` reports
// has_config=true for samplemod (exit 0 = PASS; a missing button or an un-synced
// def fails non-zero). Mirrors the seed's sync target (fixture/seed.go).
static void runExerciseSync() {
  QString kpmRoot = QString::fromLocal8Bit(qgetenv("KPM_ROOT"));
  QString defPath = kpmRoot + "/.adds/kpm/packages.d/samplemod.toml";

  waitForRows(
      [defPath]() {
        QLabel *btn = findLabelByText("Sync");
        if (!btn) {
          std::fprintf(stderr, "sim: no Sync button in the browse footer\n");
          qApp->exit(5);
          return;
        }
        std::fprintf(stderr, "sim: tapping Sync\n");
        sendClick(btn); // -> BrowseDialog::sync() -> KpmProcess::sync() runs `kpm sync`
        // Give kpm time to take the lock, re-copy the def, and release, then assert.
        QTimer::singleShot(4000, qApp, [defPath]() {
          int rc = 0;
          // (a) the local def now carries the registry's config table.
          QByteArray def = readBytes(defPath);
          if (!def.contains("[[configs]]")) {
            std::fprintf(stderr, "sim: FAIL — samplemod def has no [[configs]] after sync:\n%s\n", def.constData());
            rc = 8;
          } else {
            std::fprintf(stderr, "sim: PASS — samplemod def gained the [[configs]] table\n");
          }
          // (b) a fresh `kpm search --json` reports has_config=true for samplemod.
          QProcess sp;
          sp.start(QString::fromLocal8Bit(qgetenv("NKPM_KPM")), QStringList{"search", "--json"});
          if (!sp.waitForFinished(15000)) {
            std::fprintf(stderr, "sim: FAIL — `kpm search --json` did not finish\n");
            qApp->exit(9);
            return;
          }
          QByteArray out = sp.readAllStandardOutput();
          int idx = out.indexOf("BEGIN_JSON");
          QJsonObject payload;
          if (idx >= 0) {
            payload = QJsonDocument::fromJson(out.mid(idx + static_cast<int>(sizeof("BEGIN_JSON") - 1))).object();
          }
          bool found = false, hasConfig = false;
          for (const QJsonValue &v : payload.value("packages").toArray()) {
            QJsonObject o = v.toObject();
            if (o.value("id").toString() == "samplemod") {
              found = true;
              hasConfig = o.value("has_config").toBool();
            }
          }
          if (!found) {
            std::fprintf(stderr, "sim: FAIL — samplemod missing from `kpm search --json`\n");
            rc = 10;
          } else if (!hasConfig) {
            std::fprintf(stderr, "sim: FAIL — samplemod has_config=false after sync\n");
            rc = 10;
          } else {
            std::fprintf(stderr, "sim: PASS — search --json reports samplemod has_config=true\n");
          }
          qApp->exit(rc);
        });
      },
      []() {
        std::fprintf(stderr, "sim: timed out waiting for packages\n");
        qApp->exit(3);
      });
}

// runExerciseBadges asserts the two Wave-3 reliability badges render from the
// seeded fixture (fixture/seed.go), by exact label text like the other exercises:
//   (a) samplemod carries a seeded MissingFiles -> PackageRow's top-priority
//       "files missing" badge appears in the browse list (packagerow.cc).
//   (b) nickelnote's registry def has an older tested_fw than the seeded device
//       firmware -> its detail page shows the "Untested on your firmware" warning
//       line (detaildialog.cc, off the server-computed fw_untested).
// Read-only: no mutation, so the seeded MissingFiles never self-clears here.
static void runExerciseBadges() {
  waitForRows(
      []() {
        // (a) the browse list shows samplemod's top-priority "files missing" badge.
        if (!findLabelByText("files missing")) {
          std::fprintf(stderr, "sim: FAIL — no 'files missing' badge in the browse list\n");
          qApp->exit(5);
          return;
        }
        std::fprintf(stderr, "sim: PASS — browse list shows the 'files missing' badge\n");

        // (b) open nickelnote's detail; assert the firmware-untested warning line.
        BrowseDialog *browse = nickelstub_mainWindow()->findChild<BrowseDialog *>();
        if (!browse) {
          std::fprintf(stderr, "sim: no BrowseDialog found\n");
          qApp->exit(4);
          return;
        }
        QMetaObject::invokeMethod(browse, "openDetail", Q_ARG(QString, QStringLiteral("nickelnote")));
        QTimer::singleShot(700, qApp, []() {
          if (!findLabelByText("Untested on your firmware")) {
            std::fprintf(stderr, "sim: FAIL — nickelnote detail missing the firmware-untested warning\n");
            qApp->exit(6);
            return;
          }
          std::fprintf(stderr, "sim: PASS — nickelnote detail shows 'Untested on your firmware'\n");
          qApp->exit(0);
        });
      },
      []() {
        std::fprintf(stderr, "sim: timed out waiting for packages\n");
        qApp->exit(3);
      });
}

// runExerciseEnroll drives the "Enable self-update" flow end to end, offline:
// open the kpm detail -> tap "Enable self-update" -> DetailDialog::enableSelfUpdate()
// -> KpmProcess::adoptSelf() runs `kpm adopt-self --json`, which (best-effort refresh
// no-ops / soft-fails on the warm pre-seeded cache) records kpm's adoption identity in
// state.json. On success the post-enrol "Check for an update now?" confirmation appears;
// we REJECT it ("Not now") to keep the run offline — a check needs the network. It then
// asserts, from the sandbox, that (a) state.json gained packages.kpm.source and (b) a
// fresh `kpm search --json` kpm row reports self_configured=true (exit 0 = PASS). Mirrors
// the seed's self-enrol target (fixture/seed.go: the un-adopted kpm self row).
static void runExerciseEnroll() {
  QString kpmRoot = QString::fromLocal8Bit(qgetenv("KPM_ROOT"));
  QString statePath = kpmRoot + "/.adds/kpm/state.json";

  waitForRows(
      [statePath]() {
        BrowseDialog *browse = nickelstub_mainWindow()->findChild<BrowseDialog *>();
        if (!browse) {
          std::fprintf(stderr, "sim: no BrowseDialog found\n");
          qApp->exit(4);
          return;
        }
        QMetaObject::invokeMethod(browse, "openDetail", Q_ARG(QString, QStringLiteral("kpm")));
        QTimer::singleShot(600, qApp, [statePath]() {
          QLabel *btn = findLabelByText("Enable self-update");
          if (!btn) {
            std::fprintf(stderr, "sim: no 'Enable self-update' button on the kpm detail page\n");
            qApp->exit(5);
            return;
          }
          std::fprintf(stderr, "sim: tapping 'Enable self-update'\n");
          sendClick(btn); // -> DetailDialog::enableSelfUpdate() -> KpmProcess::adoptSelf()
          // adopt-self runs a best-effort network refresh (up to ~30s if the host is
          // truly offline) before adopting from the warm cache, so POLL for the state
          // mutation rather than a fixed wait. Reject the "Check now?" confirmation as
          // soon as it appears to keep the run offline (a check needs the network).
          int *elapsed = new int(0);
          QTimer *poll = new QTimer(qApp);
          QObject::connect(poll, &QTimer::timeout, qApp, [statePath, elapsed, poll]() {
            *elapsed += 500;
            if (g_lastConfirmation) {
              std::fprintf(stderr, "sim: rejecting the post-enrol 'Check now?' prompt (Not now)\n");
              g_lastConfirmation->reject();
            }
            // state.json only carries a kpm source AFTER adopt-self records the
            // adoption identity — no seeded package writes that source into state.
            QByteArray state = readBytes(statePath);
            bool adopted = state.contains("github.com/wolffshots/kpm");
            if (adopted) {
              poll->stop();
              delete elapsed;
              std::fprintf(stderr, "sim: PASS — state.json gained packages.kpm.source\n");
              // (b) a fresh `kpm search --json` reports self_configured=true for kpm.
              QProcess sp;
              sp.start(QString::fromLocal8Bit(qgetenv("NKPM_KPM")), QStringList{"search", "--json"});
              if (!sp.waitForFinished(15000)) {
                std::fprintf(stderr, "sim: FAIL — `kpm search --json` did not finish\n");
                qApp->exit(9);
                return;
              }
              QByteArray out = sp.readAllStandardOutput();
              int idx = out.indexOf("BEGIN_JSON");
              QJsonObject payload;
              if (idx >= 0) {
                payload = QJsonDocument::fromJson(out.mid(idx + static_cast<int>(sizeof("BEGIN_JSON") - 1))).object();
              }
              bool found = false, selfConfigured = false;
              for (const QJsonValue &v : payload.value("packages").toArray()) {
                QJsonObject o = v.toObject();
                if (o.value("id").toString() == "kpm") {
                  found = true;
                  selfConfigured = o.value("self_configured").toBool();
                }
              }
              if (!found) {
                std::fprintf(stderr, "sim: FAIL — kpm missing from `kpm search --json`\n");
                qApp->exit(10);
              } else if (!selfConfigured) {
                std::fprintf(stderr, "sim: FAIL — kpm self_configured=false after enrol\n");
                qApp->exit(10);
              } else {
                std::fprintf(stderr, "sim: PASS — search --json reports kpm self_configured=true\n");
                qApp->exit(0);
              }
            } else if (*elapsed > 40000) {
              poll->stop();
              delete elapsed;
              std::fprintf(stderr, "sim: FAIL — state.json never gained packages.kpm.source:\n%s\n", state.constData());
              qApp->exit(8);
            }
          });
          poll->start(500);
        });
      },
      []() {
        std::fprintf(stderr, "sim: timed out waiting for packages\n");
        qApp->exit(3);
      });
}

// ---- --exercise-wake ----------------------------------------------------
//
// Verifies Dialog's chrome hide/restore AND the sleep/wake ChromeGuard against
// the fake status bar + MainNavView (nickelstub). Steps: browse already open ->
// assert both bars hidden (pre-existing hideChrome behavior) -> open detail over
// it -> assert still hidden -> re-show both bars in nav-then-status order
// (mimicking Nickel's wake re-assert) -> process events -> assert the filter
// re-hid both -> close the whole UI via the close-X path -> assert both restored
// (exactly once: the filter is removed before ~Dialog restores).
static bool barsHidden() { return !nickelstub_statusBar()->isVisible() && !nickelstub_navView()->isVisible(); }
static bool barsVisible() { return nickelstub_statusBar()->isVisible() && nickelstub_navView()->isVisible(); }

static void runExerciseWake() {
  waitForRows(
      []() {
        if (!barsHidden()) {
          std::fprintf(stderr, "sim: FAIL — opening browse did not hide both bars\n");
          qApp->exit(5);
          return;
        }
        std::fprintf(stderr, "sim: PASS — opening browse hid the status bar + nav view\n");

        QList<PackageRow *> rows = nickelstub_mainWindow()->findChildren<PackageRow *>();
        sendClick(rows.first()); // open DetailDialog over the recording BrowseDialog
        QTimer::singleShot(500, qApp, []() {
          if (!barsHidden()) {
            std::fprintf(stderr, "sim: FAIL — bars reappeared when detail opened\n");
            qApp->exit(6);
            return;
          }
          std::fprintf(stderr, "sim: PASS — bars stay hidden with detail stacked on top\n");

          // Mimic Nickel's wake teardown re-asserting the home chrome: re-show the
          // nav (fires the ChromeGuard) then the status bar (caught by the guard's
          // deferred re-assert).
          nickelstub_navView()->show();
          nickelstub_statusBar()->show();
          std::fprintf(stderr, "sim: simulated wake — re-showed status bar + nav view\n");

          QTimer::singleShot(50, qApp, []() {
            if (!barsHidden()) {
              std::fprintf(stderr, "sim: FAIL — ChromeGuard did not re-hide bars after wake\n");
              qApp->exit(7);
              return;
            }
            std::fprintf(stderr, "sim: PASS — ChromeGuard re-hid both bars after wake\n");

            QMetaObject::invokeMethod(nickelstub_mainWindow(), "closeTop"); // close-X path
            QTimer::singleShot(500, qApp, []() {
              if (!barsVisible()) {
                std::fprintf(stderr, "sim: FAIL — closing did not restore both bars\n");
                qApp->exit(8);
                return;
              }
              std::fprintf(stderr, "sim: PASS — closing restored both bars (exactly once)\n");
              qApp->exit(0);
            });
          });
        });
      },
      []() {
        std::fprintf(stderr, "sim: timed out waiting for packages\n");
        qApp->exit(3);
      });
}

// pageLabelText returns the PagedStack's "Page X of Y" pagination label text (the
// only child QLabel whose text starts with "Page "); empty if none. Used by the
// reload exercise to read which page is current without touching PagedStack's
// private current/total members.
static QString pageLabelText(PagedStack *pages) {
  for (QLabel *l : pages->findChildren<QLabel *>()) {
    if (l->text().startsWith(QStringLiteral("Page "))) {
      return l->text();
    }
  }
  return QString();
}

// totalFromLabel parses N out of a "Page X of N" pagination label (0 if the label
// is single-page "Page X" or absent). PagedStack keeps its real page total private;
// the label is the observable proxy for it.
static int totalFromLabel(const QString &lbl) {
  int at = lbl.indexOf(QStringLiteral(" of "));
  return at < 0 ? 0 : lbl.mid(at + 4).toInt();
}

// runExerciseReload probes Win 2 (kpm-flash-reduction-plan) PagedStack::reload()
// on a DEEP page stack — the case the other exercises don't reach. It paginates
// the browse list, navigates to page 2 (so the stack holds [status, page1, page2]),
// then triggers a data-in-hand re-render (commit with an empty filter → reload()
// with before>2). Asserts: no blank/stale frame (rows still shown), the high→low
// stale-page delete leaves the new page correctly indexed (reset to "Page 1 of N"),
// the page count is unchanged, and prev/next still page after the swap-once reload
// (exit 0 = PASS; each failure exits non-zero).
static void runExerciseReload() {
  waitForRows(
      []() {
        BrowseDialog *browse = nickelstub_mainWindow()->findChild<BrowseDialog *>();
        PagedStack *pages = browse ? browse->findChild<PagedStack *>() : nullptr;
        if (!pages) {
          std::fprintf(stderr, "sim: no PagedStack found\n");
          qApp->exit(4);
          return;
        }
        int totalPages = totalFromLabel(pageLabelText(pages));
        if (totalPages < 2) {
          std::fprintf(stderr, "sim: FAIL — need >=2 pages to test the multi-page reload (got %d)\n", totalPages);
          qApp->exit(5);
          return;
        }
        std::fprintf(stderr, "sim: browse paginated into %d pages; navigating to page 2\n", totalPages);
        QMetaObject::invokeMethod(pages, "next"); // build + show page 2: stack now [status, page1, page2]
        QTimer::singleShot(500, qApp, [browse, pages, totalPages]() {
          QString lbl = pageLabelText(pages);
          int rows = nickelstub_mainWindow()->findChildren<PackageRow *>().size();
          if (rows < 1 || !lbl.startsWith(QStringLiteral("Page 2"))) {
            std::fprintf(stderr, "sim: FAIL — page 2 not shown (rows=%d, label='%s')\n", rows, qPrintable(lbl));
            qApp->exit(6);
            return;
          }
          std::fprintf(stderr, "sim: on '%s' with %d rows; re-rendering in place (reload from page 2)\n",
                       qPrintable(lbl), rows);
          // Data-in-hand re-render while on page 2 → PagedStack::reload() with before>2:
          // exercises the high→low delete loop and the reset-to-page-1 index math.
          QMetaObject::invokeMethod(browse, "commit"); // empty filter → all packages
          QTimer::singleShot(500, qApp, [pages, totalPages]() {
            QString lbl = pageLabelText(pages);
            int rows = nickelstub_mainWindow()->findChildren<PackageRow *>().size();
            if (rows < 1) {
              std::fprintf(stderr, "sim: FAIL — blank/stale frame after reload from page 2 (0 rows)\n");
              qApp->exit(7);
              return;
            }
            int nowTotal = totalFromLabel(lbl);
            if (nowTotal != totalPages) {
              std::fprintf(stderr, "sim: FAIL — page count changed after reload (%d -> %d)\n", totalPages, nowTotal);
              qApp->exit(8);
              return;
            }
            if (!lbl.startsWith(QStringLiteral("Page 1"))) {
              std::fprintf(stderr, "sim: FAIL — reload did not reset to page 1 (label='%s')\n", qPrintable(lbl));
              qApp->exit(9);
              return;
            }
            std::fprintf(stderr, "sim: PASS — reload from page 2 rendered %d rows, reset to '%s', %d pages intact\n",
                         rows, qPrintable(lbl), totalPages);
            QMetaObject::invokeMethod(pages, "next"); // pagination still works after the swap-once reload
            QTimer::singleShot(400, qApp, [pages]() {
              QString lbl2 = pageLabelText(pages);
              int r2 = nickelstub_mainWindow()->findChildren<PackageRow *>().size();
              if (r2 < 1 || !lbl2.startsWith(QStringLiteral("Page 2"))) {
                std::fprintf(stderr, "sim: FAIL — next() after reload broke (rows=%d, label='%s')\n", r2,
                             qPrintable(lbl2));
                qApp->exit(10);
                return;
              }
              QMetaObject::invokeMethod(pages, "prev");
              QTimer::singleShot(400, qApp, [pages]() {
                QString lbl3 = pageLabelText(pages);
                int r3 = nickelstub_mainWindow()->findChildren<PackageRow *>().size();
                if (r3 < 1 || !lbl3.startsWith(QStringLiteral("Page 1"))) {
                  std::fprintf(stderr, "sim: FAIL — prev() after reload broke (rows=%d, label='%s')\n", r3,
                               qPrintable(lbl3));
                  qApp->exit(11);
                  return;
                }
                std::fprintf(stderr, "sim: PASS — prev/next paging works after reload ('%s')\n", qPrintable(lbl3));
                qApp->exit(0);
              });
            });
          });
        });
      },
      []() {
        std::fprintf(stderr, "sim: timed out waiting for packages\n");
        qApp->exit(3);
      });
}

int main(int argc, char **argv) {
  QApplication app(argc, argv);

  int w = 758, h = 1024;
  QString screenshotDir;
  QString uninstallId;
  QString configId;
  bool exerciseSync = false;
  bool exerciseWake = false;
  bool exerciseInit = false;
  bool exerciseBadges = false;
  bool exerciseEnroll = false;
  bool exerciseReload = false;
  bool sizeGiven = false;

  QStringList args = app.arguments();
  for (int i = 1; i < args.size(); i++) {
    const QString &a = args.at(i);
    if (a == "--size" && i + 1 < args.size()) {
      QStringList wh = args.at(++i).split('x', Qt::SkipEmptyParts);
      if (wh.size() == 2) {
        w = wh.at(0).toInt();
        h = wh.at(1).toInt();
        sizeGiven = true;
      }
    } else if (a == "--exercise-reload") {
      exerciseReload = true;
    } else if (a == "--screenshot" && i + 1 < args.size()) {
      screenshotDir = args.at(++i);
    } else if (a == "--exercise-uninstall" && i + 1 < args.size()) {
      uninstallId = args.at(++i);
    } else if (a == "--exercise-config" && i + 1 < args.size()) {
      configId = args.at(++i);
    } else if (a == "--exercise-sync") {
      exerciseSync = true;
    } else if (a == "--exercise-wake") {
      exerciseWake = true;
    } else if (a == "--exercise-init") {
      exerciseInit = true;
    } else if (a == "--exercise-badges") {
      exerciseBadges = true;
    } else if (a == "--exercise-enroll") {
      exerciseEnroll = true;
    }
  }

  // The reload probe needs a paginated browse list; the default portrait size fits
  // all six fixture packages on one page. Shrink the window (unless overridden) so
  // the list spans >=2 pages and reload()'s multi-page delete path is exercised.
  if (exerciseReload && !sizeGiven) {
    w = 600;
    h = 360;
  }

  nickelstub_install(w, h);
  nickelstub_mainWindow()->show();

  BrowseDialog::show();

  if (!screenshotDir.isEmpty()) {
    runScreenshot(screenshotDir);
  } else if (!uninstallId.isEmpty()) {
    runExerciseUninstall(uninstallId);
  } else if (!configId.isEmpty()) {
    runExerciseConfig(configId);
  } else if (exerciseSync) {
    runExerciseSync();
  } else if (exerciseWake) {
    runExerciseWake();
  } else if (exerciseInit) {
    runExerciseInit();
  } else if (exerciseBadges) {
    runExerciseBadges();
  } else if (exerciseEnroll) {
    runExerciseEnroll();
  } else if (exerciseReload) {
    runExerciseReload();
  }

  return app.exec();
}
