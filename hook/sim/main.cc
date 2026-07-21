// hook/sim — desktop simulator entry point.
//
// Wires the sim Nickel shim (nickelstub) into the REAL dialog sources and opens
// BrowseDialog, so the on-device UI flows can be exercised on a PC against a
// host-built kpm binary. Flags:
//   --size WxH               main-window size (default 758x1024, Kobo portrait)
//   --screenshot <dir>       offscreen: grab browse.png + detail.png, then exit
//   --exercise-uninstall <id>  offscreen: drive the Uninstall flow end-to-end
//
// Screenshot/exercise modes are designed for QT_QPA_PLATFORM=offscreen.

#include <cstdio>
#include <cstdlib>
#include <functional>

#include <QApplication>
#include <QDir>
#include <QLabel>
#include <QMouseEvent>
#include <QPixmap>
#include <QString>
#include <QStringList>
#include <QTimer>

#include "browsedialog.h"
#include "nickelstub.h"
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

static void runScreenshot(const QString &dir) {
  QDir().mkpath(dir);
  waitForRows(
      [dir]() {
        saveGrab(dir + "/browse.png");
        QList<PackageRow *> rows = nickelstub_mainWindow()->findChildren<PackageRow *>();
        sendClick(rows.first()); // opens DetailDialog for the first package
        QTimer::singleShot(700, qApp, [dir]() {
          saveGrab(dir + "/detail.png");
          qApp->quit();
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

int main(int argc, char **argv) {
  QApplication app(argc, argv);

  int w = 758, h = 1024;
  QString screenshotDir;
  QString exerciseId;

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
      exerciseId = args.at(++i);
    }
  }

  nickelstub_install(w, h);
  nickelstub_mainWindow()->show();

  BrowseDialog::show();

  if (!screenshotDir.isEmpty()) {
    runScreenshot(screenshotDir);
  } else if (!exerciseId.isEmpty()) {
    runExerciseUninstall(exerciseId);
  }

  return app.exec();
}
