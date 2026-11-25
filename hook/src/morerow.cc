// kpm-nickelmenu-less-research-2026-11-24.md section 8 (approach B, widget B1):
// a "Package manager" row on the Nickel More page, directly above Help.
//
// No hook and no Nickel symbol. One application event filter waits for the
// Polish or Show event of the widget "helpButton". Nickel builds the More page
// only after a More tap (research section 6.3), so the row code runs only after
// a user action, not at boot. A tap calls the same function as the trigger file
// /tmp/nkpm-open, so the NickelMenu path stays as a second path.
//
// MoreRow and TouchClick are adapted from NickelModManager (MenuEntry in
// src/ui/menu.cpp, TouchClick in src/ui/controls.cpp, commit f7b5201,
// github.com/nicoverbruggen/NickelModManager):
//
//   MIT License. Copyright (c) 2026 Nico Verbruggen.
//   Permission is hereby granted, free of charge, to any person obtaining a copy
//   of this software and associated documentation files (the "Software"), to
//   deal in the Software without restriction, including without limitation the
//   rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
//   sell copies of the Software, and to permit persons to whom the Software is
//   furnished to do so, subject to the following conditions: The above
//   copyright notice and this permission notice shall be included in all
//   copies or substantial portions of the Software. THE SOFTWARE IS PROVIDED
//   "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT
//   LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR
//   PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT
//   HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN
//   ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
//   CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
//
// Changes from NMM: the kpm icon, no pressed fill (a row that opens a page
// keeps no hit state, nickel-ui-research-2026-11-17.md), no allWidgets() pass,
// a MoreView ancestor check, and the crash guard of moreguard.h.

#include <QApplication>
#include <QBoxLayout>
#include <QEvent>
#include <QImage>
#include <QLabel>
#include <QPainter>
#include <QPixmap>
#include <QPointer>
#include <QPushButton>
#include <QTimer>
#include <QTouchEvent>

#include <NickelHook.h>

#include "files.h"
#include "moreguard.h"
#include "morerow.h"

namespace {

// The build id of the crash guard. Each build of the hook runs "make clean"
// first (hook/Makefile), so each build compiles this file again.
// ponytail: an incremental build without "make clean" keeps the old id. A
// build hash from the Makefile would remove that gap.
char const kBuild[] = "qt" QT_VERSION_STR " " __DATE__ " " __TIME__;

char const kRowName[] = "nkpmMoreRow";

#if QT_VERSION < QT_VERSION_CHECK(6, 0, 0)
// Nickel on firmware 4.x delivers taps as touch events, and Qt 5.2 does not
// turn an unhandled touch into a mouse click. This makes the row take the
// touch itself and click when the finger lifts inside it.
class TouchClick : public QObject {
public:
  explicit TouchClick(QAbstractButton *button) : QObject(button), button_(button) {
    button->setAttribute(Qt::WA_AcceptTouchEvents);
    button->installEventFilter(this);
  }

protected:
  bool eventFilter(QObject *watched, QEvent *event) override {
    if (watched != button_) {
      return false;
    }
    switch (event->type()) {
    case QEvent::TouchBegin:
      event->accept();
      return true;
    case QEvent::TouchUpdate:
    case QEvent::TouchCancel:
      return true;
    case QEvent::TouchEnd: {
      QList<QTouchEvent::TouchPoint> const points = static_cast<QTouchEvent *>(event)->touchPoints();
      if (!points.isEmpty() && button_->rect().contains(points.first().pos().toPoint())) {
        button_->click();
      }
      return true;
    }
    default:
      return false;
    }
  }

private:
  QAbstractButton *button_;
};
#endif

// MoreRow copies the font, text inset, icon column, height and bottom rule of
// the Help row at paint time.
class MoreRow : public QPushButton {
public:
  MoreRow(QWidget *reference, QWidget *parent) : QPushButton(QStringLiteral("Package manager"), parent), reference_(reference) {
    setObjectName(QString::fromLatin1(kRowName));
    setFocusPolicy(Qt::NoFocus);
    setSizePolicy(QSizePolicy::Preferred, QSizePolicy::Fixed);
#if QT_VERSION < QT_VERSION_CHECK(6, 0, 0)
    new TouchClick(this);
#endif
    reference->installEventFilter(this);
  }

  QSize sizeHint() const override {
    int h = fontMetrics().height() * 3;
    if (reference_) {
      h = reference_->height() > 1 ? reference_->height() : reference_->sizeHint().height();
    }
    return QSize(QPushButton::sizeHint().width(), qMax(1, h));
  }
  QSize minimumSizeHint() const override { return sizeHint(); }

protected:
  bool eventFilter(QObject *watched, QEvent *event) override {
    if (watched == reference_ && (event->type() == QEvent::Resize || event->type() == QEvent::Polish ||
                                  event->type() == QEvent::FontChange)) {
      updateGeometry();
      update();
    }
    return QPushButton::eventFilter(watched, event);
  }

  void paintEvent(QPaintEvent *) override {
    QPainter painter(this);
    QLabel *label = reference_ ? reference_->findChild<QLabel *>(QStringLiteral("label")) : nullptr;
    QLabel *glyph = reference_ ? reference_->findChild<QLabel *>(QStringLiteral("pixmap")) : nullptr;
    QColor const ink = label ? label->palette().color(label->foregroundRole()) : palette().color(QPalette::WindowText);

    int left = qMax(16, height() / 4);
    if (label && label->isVisible()) {
      // Nickel indents its labels with style sheet padding, which contentsRect
      // gives. QLabel::paintEvent adds half an "x" when the indent is not set
      // and the label has a frame, which a style sheet with padding gives.
      int indent = label->indent();
      if (indent < 0) {
        QFontMetrics const fm(label->font());
#if QT_VERSION >= QT_VERSION_CHECK(5, 11, 0)
        int const x = fm.horizontalAdvance(QLatin1Char('x'));
#else
        int const x = fm.width(QLatin1Char('x'));
#endif
        indent = label->frameWidth() > 0 ? x / 2 : 0;
      }
      left = label->mapTo(reference_, label->contentsRect().topLeft()).x() + indent;
    }
    if (glyph && glyph->isVisible()) {
      // Nickel's own glyphs fill about three fifths of the icon column.
      QRect const column(glyph->mapTo(reference_, QPoint()), glyph->size());
      int const side = qMax(1, qRound(qMin(column.width(), column.height()) * .6));
      // Convert after the smooth scale: the scale can give a premultiplied image.
      QImage icon = QImage(QString::fromUtf8(Files::icon))
                        .scaled(side, side, Qt::KeepAspectRatio, Qt::SmoothTransformation)
                        .convertToFormat(QImage::Format_ARGB32);
      if (ink.lightness() > 127) {
        // A dark page: the icon is black on transparent. Format_ARGB32 is not
        // premultiplied, so the transparent pixels stay transparent.
        icon.invertPixels(QImage::InvertRgb);
      }
      painter.drawImage(column.center().x() - icon.width() / 2, column.center().y() - icon.height() / 2, icon);
    }
    painter.setFont(label ? label->font() : font());
    painter.setPen(ink);
    painter.drawText(QRect(left, 0, qMax(0, width() - left), height()), Qt::AlignLeft | Qt::AlignVCenter, text());
    painter.fillRect(QRect(0, height() - 1, width(), 1), ink);
  }

private:
  QPointer<QWidget> reference_;
};

class MoreRowFilter : public QObject {
public:
  MoreRowFilter(void (*open)(), QObject *parent) : QObject(parent), open_(open) {}

protected:
  bool eventFilter(QObject *watched, QEvent *event) override {
    QEvent::Type const type = event->type();
    if ((type == QEvent::Polish || type == QEvent::Show) && watched->isWidgetType() &&
        watched->objectName() == QLatin1String("helpButton")) {
      insert(static_cast<QWidget *>(watched));
    }
    return false;
  }

private:
  void insert(QWidget *help) {
    QWidget *parent = help->parentWidget();
    QBoxLayout *layout = parent ? qobject_cast<QBoxLayout *>(parent->layout()) : nullptr;
    if (!layout || layout->indexOf(help) < 0 ||
        parent->findChild<QWidget *>(QString::fromLatin1(kRowName), Qt::FindDirectChildrenOnly)) {
      return;
    }
    // Only the More page: another page with a "helpButton" gets no row.
    QWidget *page = parent;
    while (page && !page->inherits("MoreView")) {
      page = page->parentWidget();
    }
    if (!page) {
      return;
    }

    if (!armed_) {
      armed_ = true;
      if (nkpm_moreguard_arm(NKPM_MORE_MARKER, kBuild) != 0) {
        nh_log("NickelKPM: could not write %s, the More row has no crash guard this boot", NKPM_MORE_MARKER);
      }
      QTimer::singleShot(3000, [] {
        nkpm_moreguard_clear(NKPM_MORE_MARKER);
        nh_log("NickelKPM: More row ok for 3 s, crash guard cleared");
      });
    }

    int const index = layout->indexOf(help);
    MoreRow *row = new MoreRow(help, parent);
    layout->insertWidget(index, row);
    // A layout shows a new child of a visible parent one event loop pass late.
    // Show it now, so it is in the first paint of the page.
    row->show();
    void (*open)() = open_;
    QObject::connect(row, &QPushButton::clicked, [open] { open(); });
    nh_log("NickelKPM: More row inserted at index %d of %d", index, layout->count());
  }

  void (*open_)();
  bool armed_ = false;
};

} // namespace

void nkpm_morerow_init(void (*open)()) {
  if (!nkpm_moreguard_allow(NKPM_MORE_MARKER, kBuild)) {
    nh_log("NickelKPM: More row off: %s is left from a crash of this build", NKPM_MORE_MARKER);
    return;
  }
  QCoreApplication *app = QCoreApplication::instance();
  if (!app) {
    nh_log("NickelKPM: More row off: no application object at init");
    return;
  }
  app->installEventFilter(new MoreRowFilter(open, app));
  nh_log("NickelKPM: More row filter installed (build %s)", kBuild);
}
