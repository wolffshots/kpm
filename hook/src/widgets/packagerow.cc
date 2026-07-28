// NICKEL-UI.md §5 — PackageRow. Adapted from NickelHardcover's bookrow.cc (MIT)
// per §0; simplified to a text-only, tappable row (no cover art / buttons).

#include <QHBoxLayout>
#include <QJsonArray>
#include <QVBoxLayout>

#include "../nkpm.h"
#include "elidedlabel.h"
#include "label.h"
#include "packagerow.h"

QString PackageRow::badgeText(const QJsonObject &json) {
  // §A (post-install manifest verification): a non-empty missing_files array is
  // the TOP-priority badge — the package promoted to installed but one or more
  // of its manifest members never landed (an rcS failure or foreign tgz). It
  // outranks staged/installed/update: those describe intent, this describes a
  // broken install. missing_files is null/absent when the package verified clean
  // (search --json, server-computed), so toArray() yields empty and this is skipped.
  if (!json.value("missing_files").toArray().isEmpty()) {
    return QStringLiteral("files missing");
  }
  // §5 state table. installed is JSON null (→ empty string) when not installed;
  // latest/update are merged in from `kpm check --json` (absent until a check).
  if (json.value("staged").toBool()) {
    return QStringLiteral("● reboot pending"); // ●
  }
  // INTEGRITY.md §4/§6.1: conflicts_active lists every package that conflicts with
  // this one in EITHER direction and is installed or staged on the device right
  // now. Not installed ⇒ `kpm install` hard-refuses (§4.3); already installed ⇒
  // the §4.5 pair, reported but never blocked. Either way the collision is the
  // fact worth the badge slot, so it outranks the version line. It ranks BELOW
  // staged because a pending reboot is the more immediate action (and an installed
  // half of a reported pair can legitimately be mid-update). The field is
  // server-normalized to [] rather than null, so an older kpm that omits it
  // entirely also lands on an empty array here.
  if (!json.value("conflicts_active").toArray().isEmpty()) {
    return QStringLiteral("conflict");
  }
  // Self-update enrolment (kpm-self-enrol-plan §5): the kpm row's self-update is
  // not wired up. Ranks below files-missing/staged (those describe a broken or
  // pending install) but above the normal not-installed/update/current lines — an
  // un-adopted kpm otherwise renders a healthy "0.7.0 ✓". Gated on the
  // server-computed is_self && !self_configured; both false for every non-kpm row.
  if (json.value("is_self").toBool() && !json.value("self_configured").toBool()) {
    return QStringLiteral("self-update off");
  }
  QString installed = json.value("installed").toString();
  if (installed.isEmpty()) {
    return QStringLiteral("not installed");
  }
  QString latest = json.value("latest").toString();
  if (json.value("update").toBool()) {
    if (!latest.isEmpty() && latest != installed) {
      return installed + QStringLiteral(" → ") + latest + QStringLiteral(" ↑"); // → ↑
    }
    // INTEGRITY.md §6.3: a def marked update_check = "rolling" republishes under a
    // permanently constant tag, so `check` reports update=true with latest EQUAL to
    // installed. Rendering "latest → latest ↑" would read as a bug; phrase it off
    // the new `rolling` flag instead, matching the CLI's
    // "UPDATE AVAILABLE (release rebuilt)" line (§3.7). A same-tag update on a
    // non-rolling def is not expressible (tagsEqual gates it), so that combination
    // falls through to the current-version badge rather than inventing a state.
    if (json.value("rolling").toBool()) {
      return installed + QStringLiteral(" rebuilt ↑"); // ↑
    }
  }
  return installed + QStringLiteral(" ✓"); // ✓
}

PackageRow::PackageRow(QJsonObject json, QWidget *parent)
    : QFrame(parent), id(json.value("id").toString()) {
  setStyleSheet(R"(
    PackageRow {
      border-top: 1px solid #666666;
      padding: 14px;
    }
    PackageRow[noBorder=true] {
      border-top-width: 0;
    }
  )");

  QVBoxLayout *layout = new QVBoxLayout(this);
  layout->setContentsMargins(0, 0, 0, 0);
  layout->setSpacing(2);

  QHBoxLayout *top = new QHBoxLayout();
  top->setContentsMargins(0, 0, 0, 0);
  layout->addLayout(top);

  QString name = json.value("name").toString();
  if (name.isEmpty()) {
    name = id;
  }
  Label *nameLabel = new Label(Label::Large, name);
  nameLabel->setStyleSheet(nameLabel->styleSheet() + "\nLabel { font-weight: bold; }");
  top->addWidget(nameLabel, 1);

  // Kept as a member so a post-action refresh can repaint the badge alone rather
  // than rebuilding the page (flash-reduction win 4).
  badge = new Label(Label::Small, badgeText(json));
  badge->setAlignment(Qt::AlignRight | Qt::AlignVCenter);
  top->addWidget(badge, 0, Qt::AlignRight);

  // A Nickel touch button opens the detail dialog: Kobo does not deliver a
  // plain QFrame a mouseReleaseEvent, so the tap must go through tapped().
  N3ButtonLabel *manage = construct_N3ButtonLabel(this);
  manage->setText("Manage");
  top->addWidget(manage, 0, Qt::AlignRight);
  QObject::connect(manage, SIGNAL(tapped(bool)), this, SLOT(manageTapped()));

  QString subtitle = json.value("description").toString();
  if (subtitle.isEmpty()) {
    subtitle = json.value("source").toString();
  }
  layout->addWidget(new ElidedLabel(Label::Small, subtitle));
}

// setBadgeText repaints the state badge in place (flash-reduction win 4). No-op
// when the text is unchanged, so a caller that over-collects rows cannot cause a
// redundant repaint. The badge is the only part of the row a post-action refresh
// can change without the row itself being rebuilt — BrowseDialog falls back to a
// full page rebuild for anything else (name, description/source, the row set).
void PackageRow::setBadgeText(const QString &text) {
  if (badge != nullptr && badge->text() != text) {
    badge->setText(text);
  }
}

void PackageRow::manageTapped() { selected(id); }
