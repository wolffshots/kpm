// CONFIG.md §4 — ConfigDialog rows. Structure mirrors packagerow.cc (MIT-derived
// per NICKEL-UI.md §0): a tappable QFrame whose tap is routed through an
// N3ButtonLabel, since Kobo touch input never reaches a bare QFrame.

#include <QHBoxLayout>
#include <QVBoxLayout>

#include "../nkpm.h"
#include "configrow.h"
#include "elidedlabel.h"
#include "label.h"

// Masked rendering for sensitive values — matches the human-output mask kpm uses
// (cmd_config.go maskedValue). The real value still rides in the JSON so the
// dialog can pre-fill the editor; only the on-screen row is masked (CONFIG.md §3.3).
static const QString kMasked = QStringLiteral("••••"); // ••••

// ---- ConfigFileRow ------------------------------------------------------

ConfigFileRow::ConfigFileRow(QJsonObject file, int idx, QWidget *parent) : QFrame(parent), index(idx) {
  setStyleSheet(R"(
    ConfigFileRow {
      border-top: 1px solid #666666;
      padding: 14px;
    }
  )");

  QVBoxLayout *layout = new QVBoxLayout(this);
  layout->setContentsMargins(0, 0, 0, 0);
  layout->setSpacing(2);

  QHBoxLayout *top = new QHBoxLayout();
  top->setContentsMargins(0, 0, 0, 0);
  layout->addLayout(top);

  QString name = file.value("name").toString();
  if (name.isEmpty()) {
    name = file.value("path").toString();
  }
  Label *nameLabel = new Label(Label::Large, name);
  nameLabel->setStyleSheet(nameLabel->styleSheet() + "\nLabel { font-weight: bold; }");
  top->addWidget(nameLabel, 1);

  // State badge: created files show their reload mode; missing files show whether
  // an edit can still create them (CONFIG.md §4).
  QString badgeText;
  if (!file.value("editable").toBool(true)) {
    badgeText = QStringLiteral("edit over USB");
  } else if (file.value("exists").toBool()) {
    badgeText = file.value("reload").toString();
  } else if (file.value("can_create").toBool()) {
    badgeText = QStringLiteral("not created");
  } else {
    badgeText = QStringLiteral("not created (read-only)");
  }
  Label *badge = new Label(Label::Small, badgeText);
  badge->setAlignment(Qt::AlignRight | Qt::AlignVCenter);
  top->addWidget(badge, 0, Qt::AlignRight);

  // Only editable declarations get an Open button; a non-editable one (e.g. a
  // future dir format) is view-only with the "edit over USB" badge above.
  if (file.value("editable").toBool(true)) {
    N3ButtonLabel *open = construct_N3ButtonLabel(this);
    open->setText("Open");
    top->addWidget(open, 0, Qt::AlignRight);
    QObject::connect(open, SIGNAL(tapped(bool)), this, SLOT(openTapped()));
  }

  QString subtitle = file.value("description").toString();
  if (subtitle.isEmpty()) {
    subtitle = file.value("path").toString();
  }
  layout->addWidget(new ElidedLabel(Label::Small, subtitle));
}

void ConfigFileRow::openTapped() { selected(index); }

// ---- ConfigEntryRow -----------------------------------------------------

ConfigEntryRow::ConfigEntryRow(QJsonObject e, QString format, QWidget *parent) : QFrame(parent), entry(e) {
  setStyleSheet(R"(
    ConfigEntryRow {
      border-top: 1px solid #666666;
      padding: 12px 14px;
    }
  )");

  QHBoxLayout *layout = new QHBoxLayout(this);
  layout->setContentsMargins(0, 0, 0, 0);
  layout->setSpacing(8);

  if (format == QStringLiteral("ini")) {
    // "Section · Key" (bold) + right-aligned value; sensitive values render ••••.
    QString key = entry.value("key").toString();
    QString section = entry.value("section").toString();
    QString labelText = section.isEmpty() ? key : section + QStringLiteral(" · ") + key; // ·
    Label *keyLabel = new Label(Label::Small, labelText);
    keyLabel->setStyleSheet(keyLabel->styleSheet() + "\nLabel { font-weight: bold; }");
    layout->addWidget(keyLabel, 1);

    QString value = entry.value("sensitive").toBool() ? kMasked : entry.value("value").toString();
    Label *valueLabel = new Label(Label::Small, value);
    valueLabel->setAlignment(Qt::AlignRight | Qt::AlignVCenter);
    layout->addWidget(valueLabel, 1, Qt::AlignRight);
  } else {
    // text: the line's content, elided. Prefix the 1-based line number so blank
    // lines still present a tap target and the mapping to the file is visible.
    QString content = entry.value("value").toString();
    if (content.isEmpty()) {
      content = QStringLiteral("(blank line)");
    }
    layout->addWidget(new ElidedLabel(Label::Small, content), 1);
  }

  N3ButtonLabel *edit = construct_N3ButtonLabel(this);
  edit->setText("Edit");
  layout->addWidget(edit, 0, Qt::AlignRight);
  QObject::connect(edit, SIGNAL(tapped(bool)), this, SLOT(editTapped()));
}

void ConfigEntryRow::editTapped() { selected(entry); }
