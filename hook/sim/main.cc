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
        saveGrab(dir + "/browse.png");
        QList<PackageRow *> rows = nickelstub_mainWindow()->findChildren<PackageRow *>();
        sendClick(rows.first()); // opens DetailDialog for the first package
        QTimer::singleShot(700, qApp, [dir]() {
          saveGrab(dir + "/detail.png");
          // ConfigDialog rendering: nickelclock is single-file/ini (entries page
          // + editor), nickelnote is multi-file/text (the file picker). Open them
          // directly — the detail → Settings wiring is covered by --exercise-config.
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
                  qApp->quit();
                });
              });
            });
          });
        });
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

int main(int argc, char **argv) {
  QApplication app(argc, argv);

  int w = 758, h = 1024;
  QString screenshotDir;
  QString uninstallId;
  QString configId;
  bool exerciseSync = false;
  bool exerciseWake = false;
  bool exerciseInit = false;

  QStringList args = app.arguments();
  for (int i = 1; i < args.size(); i++) {
    const QString &a = args.at(i);
    if (a == "--size" && i + 1 < args.size()) {
      QStringList wh = args.at(++i).split('x', Qt::SkipEmptyParts);
      if (wh.size() == 2) {
        w = wh.at(0).toInt();
        h = wh.at(1).toInt();
      }
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
    }
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
  }

  return app.exec();
}
