// hook/sim — desktop simulator entry point.
//
// Wires the sim Nickel shim (nickelstub) into the REAL dialog sources and opens
// BrowseDialog, so the on-device UI flows can be exercised on a PC against a
// host-built kpm binary. Flags:
//   --size WxH               main-window size (default 758x1024, Kobo portrait)
//   --screenshot <dir>       offscreen: grab browse/detail/config png's, then exit
//   --exercise-uninstall <id>  offscreen: drive the Uninstall flow end-to-end
//   --exercise-config <id>     offscreen: drive Settings → edit → Save end-to-end
//
// Screenshot/exercise modes are designed for QT_QPA_PLATFORM=offscreen.

#include <cstdio>
#include <cstdlib>
#include <functional>

#include <QApplication>
#include <QDir>
#include <QFile>
#include <QLabel>
#include <QLineEdit>
#include <QMouseEvent>
#include <QPixmap>
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
                qApp->quit();
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

int main(int argc, char **argv) {
  QApplication app(argc, argv);

  int w = 758, h = 1024;
  QString screenshotDir;
  QString uninstallId;
  QString configId;

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
  }

  return app.exec();
}
