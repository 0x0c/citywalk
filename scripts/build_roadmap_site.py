#!/usr/bin/env python3
"""Build the roadmap site that GitHub Pages publishes.

The roadmap's source of truth is the committed markdown under ``roadmaps/CW-NNNN-<slug>/`` — the
``CW-METADATA`` block in each file, the ``# `` heading, the first paragraph of *Introduction*, and
the checklist under *Progress*. This script reads that tree and renders it as a static, bilingual
site: one page per language, no framework, no network at runtime.

The whole site is a **build artifact, never committed**. It is regenerated from the live tree on
every build, so the published page can never drift from the roadmap it describes.

Usage::

    python3 scripts/build_roadmap_site.py                 # write ./site
    python3 scripts/build_roadmap_site.py --out DIR       # write elsewhere
    python3 scripts/build_roadmap_site.py --branch main   # branch the GitHub links point at
    python3 scripts/build_roadmap_site.py --no-git        # skip the Git date lookup

Only facts the roadmap carries are shown. A topic's percentage is the share of its items whose
``Status`` is ``Implemented``; an item's progress figure is the share of its *Progress* boxes that
are ticked. Neither number is invented — both have a source of truth in the tree.
"""

from __future__ import annotations

import argparse
import html
import json
import re
import subprocess
import sys
import unicodedata
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ROADMAPS = ROOT / "roadmaps"
DEFAULT_OUT = ROOT / "site"
DEFAULT_REPO = "0x0c/citywalk"
DEFAULT_BRANCH = "main"

LANGS = ("en", "ja")


class BuildError(Exception):
    """A malformed or inconsistent roadmap item — the build fails rather than publishing it."""


# --------------------------------------------------------------------------------------------
# Vocabulary
#
# Status and Topic are closed sets written in two languages. Each entry maps the words that appear
# in the files to one stable key, so the English and the Japanese file of an item can be checked
# against each other and so the generated markup never carries a language-specific value.
# --------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Status:
    key: str
    label: dict[str, str]  # what a badge or a chip shows
    raw: dict[str, str]  # what the metadata block writes
    light: str
    dark: str


# Four hues from the reference categorical palette, validated as a set against both surfaces with
# every pair in play (the relationship map puts all four on screen at once): green, yellow, blue,
# magenta. Every use pairs the hue with its written label, so colour never carries the status alone.
STATUSES: tuple[Status, ...] = (
    Status(
        "implemented",
        {"en": "Implemented", "ja": "実装済み"},
        {"en": "Implemented", "ja": "実装済み"},
        "#008300",
        "#008300",
    ),
    Status(
        "in-progress",
        {"en": "In progress", "ja": "実装中"},
        {"en": "In progress", "ja": "実装中"},
        "#eda100",
        "#c98500",
    ),
    Status(
        "proposal",
        {"en": "Proposal", "ja": "提案"},
        {"en": "Proposal", "ja": "提案"},
        "#2a78d6",
        "#3987e5",
    ),
    Status(
        "deferred",
        {"en": "Deferred", "ja": "保留"},
        {"en": "Proposal (deferred)", "ja": "提案（保留）"},
        "#e87ba4",
        "#d55181",
    ),
)


@dataclass(frozen=True)
class Topic:
    key: str
    label: dict[str, str]


# The browsing order the roadmap README uses.
TOPICS: tuple[Topic, ...] = (
    Topic("delivery-model", {"en": "Delivery model", "ja": "配信モデル"}),
    Topic("targeting", {"en": "Targeting", "ja": "ターゲティング"}),
    Topic("display-governance", {"en": "Display governance", "ja": "表示統制"}),
    Topic("measurement", {"en": "Measurement", "ja": "効果測定"}),
    Topic("platform", {"en": "Platform", "ja": "プラットフォーム"}),
)

STATUS_BY_KEY = {s.key: s for s in STATUSES}
TOPIC_BY_KEY = {t.key: t for t in TOPICS}
STATUS_FROM_RAW = {(lang, s.raw[lang]): s.key for s in STATUSES for lang in LANGS}
TOPIC_FROM_RAW = {(lang, t.label[lang]): t.key for t in TOPICS for lang in LANGS}

# Metadata field names, per language. Only the fields the site reads are listed.
FIELDS = {
    "status": {"en": "Status", "ja": "状態"},
    "topic": {"en": "Topic", "ja": "トピック"},
    "author": {"en": "Author", "ja": "提案者"},
    "related": {"en": "Related", "ja": "関連"},
}
INTRO_HEADING = {"en": "Introduction", "ja": "はじめに"}
PROGRESS_HEADING = {"en": "Progress", "ja": "進捗"}


# --------------------------------------------------------------------------------------------
# Parsing
# --------------------------------------------------------------------------------------------

ID_RE = re.compile(r"CW-\d{4}")
DIR_RE = re.compile(r"^(CW-\d{4})-(?P<slug>[a-z0-9-]+)$")
METADATA_RE = re.compile(r"<!-- CW-METADATA -->(?P<body>.*?)<!-- /CW-METADATA -->", re.DOTALL)
ROW_RE = re.compile(r"^\|(?P<cells>.*)\|\s*$")
CHECKBOX_RE = re.compile(r"^- \[(?P<mark>[ xX])\]\s+(?P<text>.*)$")
LINK_RE = re.compile(r"\[(?P<text>[^\]]*)\]\([^)]*\)")


def plain(text: str) -> str:
    """Markdown inline syntax reduced to the words a reader sees."""
    text = LINK_RE.sub(lambda m: m.group("text"), text)
    text = text.replace("`", "")
    text = re.sub(r"\*\*(.+?)\*\*", r"\1", text)
    text = re.sub(r"(?<!\*)\*(.+?)\*(?!\*)", r"\1", text)
    return re.sub(r"\s+", " ", text).strip()


@dataclass
class Doc:
    """One language file of one item."""

    lang: str
    path: Path
    title: str
    intro: str
    status_key: str
    topic_key: str
    author: str
    author_url: str
    related: list[str]
    checklist: list[tuple[bool, str]]


@dataclass
class Item:
    """One CW item: its identifier, its two language files, and the facts both agree on."""

    id: str
    slug: str
    docs: dict[str, Doc]
    created: str | None = None
    updated: str | None = None
    order: int = 0
    related: list[str] = field(default_factory=list)

    @property
    def status_key(self) -> str:
        return self.docs["en"].status_key

    @property
    def topic_key(self) -> str:
        return self.docs["en"].topic_key

    @property
    def done(self) -> int:
        return sum(1 for checked, _ in self.docs["en"].checklist if checked)

    @property
    def total(self) -> int:
        return len(self.docs["en"].checklist)

    @property
    def number(self) -> str:
        """The identifier's digits alone, for a compact label."""
        return self.id.split("-")[1]


def _section(text: str, heading: str) -> str:
    """The body of one ``## `` section, or the empty string when the heading is absent."""
    match = re.search(
        rf"^## {re.escape(heading)}\s*$(?P<body>.*?)(?=^## |\Z)", text, re.MULTILINE | re.DOTALL
    )
    return match.group("body") if match else ""


def _first_paragraph(body: str) -> str:
    """The first prose paragraph of a section, with markdown flattened."""
    for block in re.split(r"\n\s*\n", body.strip()):
        block = block.strip()
        if not block or block.startswith((">", "```", "|", "-", "*")):
            continue
        return plain(block)
    return ""


def _metadata(text: str, path: Path) -> dict[str, str]:
    """The ``CW-METADATA`` table as a field-to-value mapping, with emphasis stripped."""
    match = METADATA_RE.search(text)
    if not match:
        raise BuildError(f"{path}: no CW-METADATA block")
    fields: dict[str, str] = {}
    for line in match.group("body").splitlines():
        row = ROW_RE.match(line.strip())
        if not row:
            continue
        cells = [cell.strip() for cell in row.group("cells").split("|")]
        if len(cells) != 2 or set(cells[0]) <= {"-", ":"}:
            continue
        fields[cells[0]] = cells[1].replace("**", "").strip()
    if not fields:
        raise BuildError(f"{path}: CW-METADATA block holds no rows")
    return fields


def _checklist(body: str) -> list[tuple[bool, str]]:
    """The ``- [ ]`` boxes of a *Progress* section, each with its text joined across wrapped lines."""
    boxes: list[tuple[bool, str]] = []
    pending: list[str] = []
    checked = False
    for line in body.splitlines():
        box = CHECKBOX_RE.match(line)
        if box:
            if pending:
                boxes.append((checked, plain(" ".join(pending))))
            checked = box.group("mark").lower() == "x"
            pending = [box.group("text")]
        elif pending and line.startswith(("  ", "\t")) and line.strip():
            pending.append(line.strip())
        elif pending and not line.strip():
            boxes.append((checked, plain(" ".join(pending))))
            pending = []
    if pending:
        boxes.append((checked, plain(" ".join(pending))))
    return boxes


def parse_doc(path: Path, lang: str, item_id: str) -> Doc:
    text = path.read_text(encoding="utf-8")

    heading = re.search(r"^# (?P<title>.+)$", text, re.MULTILINE)
    if not heading:
        raise BuildError(f"{path}: no '# ' heading")
    # Headings read "CW-0001 — Title"; the identifier is shown separately, so drop it here.
    title = re.sub(rf"^{re.escape(item_id)}\s*[—–-]\s*", "", heading.group("title").strip())

    fields = _metadata(text, path)
    missing = [
        FIELDS[name][lang] for name in ("status", "topic") if FIELDS[name][lang] not in fields
    ]
    if missing:
        raise BuildError(f"{path}: CW-METADATA is missing {', '.join(missing)}")

    raw_status = fields[FIELDS["status"][lang]]
    status_key = STATUS_FROM_RAW.get((lang, raw_status))
    if status_key is None:
        known = ", ".join(sorted(s.raw[lang] for s in STATUSES))
        raise BuildError(f"{path}: unknown Status {raw_status!r} (known: {known})")

    raw_topic = fields[FIELDS["topic"][lang]]
    topic_key = TOPIC_FROM_RAW.get((lang, raw_topic))
    if topic_key is None:
        known = ", ".join(t.label[lang] for t in TOPICS)
        raise BuildError(f"{path}: unknown Topic {raw_topic!r} (known: {known})")

    author_field = fields.get(FIELDS["author"][lang], "")
    author_link = re.search(r"\[(?P<name>[^\]]+)\]\((?P<url>[^)]+)\)", author_field)
    author = author_link.group("name") if author_link else plain(author_field)
    author_url = author_link.group("url") if author_link else ""

    related = fields.get(FIELDS["related"][lang], "")
    related_ids = [rid for rid in dict.fromkeys(ID_RE.findall(related)) if rid != item_id]

    return Doc(
        lang=lang,
        path=path,
        title=title,
        intro=_first_paragraph(_section(text, INTRO_HEADING[lang])),
        status_key=status_key,
        topic_key=topic_key,
        author=author,
        author_url=author_url,
        related=related_ids,
        checklist=_checklist(_section(text, PROGRESS_HEADING[lang])),
    )


def git_dates(directory: Path) -> tuple[str | None, str | None]:
    """The first and the last commit date touching an item's directory, as UTC ISO strings.

    A shallow clone reports both as the single checked-out commit's day, so the workflow that
    publishes the site checks out the full history. When Git is unavailable the dates are dropped
    rather than guessed, and the columns that show them read as unknown.
    """
    try:
        out = subprocess.run(
            ["git", "log", "--format=%cI", "--", directory.name],
            cwd=directory.parent,
            capture_output=True,
            text=True,
            check=True,
        ).stdout.split()
    except (OSError, subprocess.CalledProcessError):
        return None, None
    if not out:
        return None, None
    return out[-1], out[0]


def load_items(roadmaps: Path, with_dates: bool = True) -> list[Item]:
    items: list[Item] = []
    seen: dict[str, Path] = {}
    for directory in sorted(p for p in roadmaps.iterdir() if p.is_dir()):
        match = DIR_RE.match(directory.name)
        if not match:
            raise BuildError(f"{directory}: directory name is not CW-NNNN-<slug>")
        item_id, slug = match.group(1), match.group("slug")
        if item_id in seen:
            raise BuildError(f"{directory}: identifier {item_id} already used by {seen[item_id]}")
        seen[item_id] = directory

        docs: dict[str, Doc] = {}
        for lang in LANGS:
            suffix = "" if lang == "en" else "-ja"
            path = directory / f"{item_id}-{slug}{suffix}.md"
            if not path.exists():
                raise BuildError(f"{path}: missing {lang} file")
            docs[lang] = parse_doc(path, lang, item_id)

        for name in ("status_key", "topic_key"):
            en_value, ja_value = getattr(docs["en"], name), getattr(docs["ja"], name)
            if en_value != ja_value:
                raise BuildError(
                    f"{directory}: {name} differs between languages ({en_value} vs {ja_value})"
                )
        if docs["en"].related != docs["ja"].related:
            raise BuildError(f"{directory}: Related differs between languages")

        item = Item(id=item_id, slug=slug, docs=docs, related=list(docs["en"].related))
        if with_dates:
            item.created, item.updated = git_dates(directory)
        items.append(item)

    if not items:
        raise BuildError(f"{roadmaps}: no CW items found")

    known = {item.id for item in items}
    for item in items:
        for rid in item.related:
            if rid not in known:
                raise BuildError(f"{item.id}: Related names {rid}, which does not exist")
    for index, item in enumerate(sorted(items, key=lambda i: i.id)):
        item.order = index
    return sorted(items, key=lambda i: i.id)


# --------------------------------------------------------------------------------------------
# Text helpers
# --------------------------------------------------------------------------------------------

esc = html.escape


def display_width(text: str) -> int:
    """A string's width in half-width units, so a Japanese label truncates where it looks right."""
    return sum(2 if unicodedata.east_asian_width(ch) in "WF" else 1 for ch in text)


def truncate(text: str, limit: int) -> str:
    if display_width(text) <= limit:
        return text
    width = 0
    out: list[str] = []
    for ch in text:
        step = 2 if unicodedata.east_asian_width(ch) in "WF" else 1
        if width + step > limit - 1:
            break
        out.append(ch)
        width += step
    return "".join(out).rstrip() + "…"


def pct(part: int, whole: int) -> int:
    return round(100 * part / whole) if whole else 0


# --------------------------------------------------------------------------------------------
# Interface strings
# --------------------------------------------------------------------------------------------

STRINGS: dict[str, dict[str, str]] = {
    "en": {
        "html_lang": "en",
        "title": "citywalk roadmap",
        "description": (
            "Every design decision behind citywalk's in-app message platform, on one board: "
            "status, topic, progress, and how the items relate."
        ),
        "brand": "citywalk",
        "eyebrow": "Server backend",
        "h1": "Roadmap",
        "lede": (
            "Every design decision behind citywalk's in-app message platform, on one board. Each "
            "item is an architecture decision record: what we are building, why, and what we "
            "rejected. Status, topic, and progress are read straight from the files under "
            "roadmaps/, and the page is regenerated on every build, so it cannot drift from the "
            "roadmap it describes."
        ),
        "other_lang": "日本語",
        "other_lang_full": "Japanese",
        "github": "GitHub",
        "theme": "Theme",
        "theme_auto": "Auto",
        "theme_light": "Light",
        "theme_dark": "Dark",
        "skip": "Skip to the roadmap",
        "overview": "Overview",
        "hero_label": "roadmap items",
        "hero_sub": "across {topics} topics",
        "composition": "Status composition",
        "composition_alt": "Every roadmap item by status: {parts}.",
        "checklist": "Work items completed",
        "checklist_note": "{done} of {total} boxes ticked across every item's Progress section",
        "search_label": "Search roadmap items",
        "search_placeholder": "Search by ID, title, topic, or status…",
        "filter_status": "Status",
        "filter_topic": "Topic",
        "view_label": "Choose a layout",
        "view_cards": "Cards",
        "view_table": "Table",
        "view_map": "Map",
        "implemented_of": "{done}/{total} implemented",
        "items_count": "{count} items",
        "col_id": "ID",
        "col_title": "Item",
        "col_topic": "Topic",
        "col_status": "Status",
        "col_progress": "Progress",
        "col_updated": "Updated",
        "related": "Related",
        "progress_boxes": "{done}/{total} work items",
        "checklist_summary": "Progress checklist",
        "map_caption": (
            "Items sit left to right by identifier and group into rows by topic. A line joins two "
            "items that name each other under Related."
        ),
        "map_hint": "Point at an item to read its title. Selecting one opens its full record.",
        "map_alt": "A map of the roadmap items, grouped by topic and joined by their relations.",
        "empty_query": "No roadmap item matches “{query}”.",
        "empty_filters": "No roadmap item matches the current filters.",
        "unknown_date": "—",
        "footer_source": "Generated from the roadmaps/ directory of",
        "footer_readme": "How the roadmap works",
        "footer_requirements": "Requirement catalog",
        "updated_prefix": "Roadmap last updated",
    },
    "ja": {
        "html_lang": "ja",
        "title": "citywalk ロードマップ",
        "description": (
            "citywalk のアプリ内メッセージ配信基盤について、設計上の決定を1つの画面にまとめました。"
            "状態、トピック、進捗、項目どうしの関連を見渡せます。"
        ),
        "brand": "citywalk",
        "eyebrow": "サーババックエンド",
        "h1": "ロードマップ",
        "lede": (
            "citywalk のアプリ内メッセージ配信基盤について、設計上の決定を1つの画面にまとめました。"
            "各項目はアーキテクチャ決定記録であり、何を作るのか、なぜそうするのか、何を採らなかったのかを"
            "記録します。状態、トピック、進捗は roadmaps/ 配下のファイルから直接読み取ります。"
            "ビルドのたびに生成し直すため、記録と表示がずれることはありません。"
        ),
        "other_lang": "English",
        "other_lang_full": "英語",
        "github": "GitHub",
        "theme": "配色",
        "theme_auto": "自動",
        "theme_light": "明るい",
        "theme_dark": "暗い",
        "skip": "ロードマップ本体へ移動",
        "overview": "概要",
        "hero_label": "ロードマップ項目",
        "hero_sub": "{topics}つのトピック",
        "composition": "状態の内訳",
        "composition_alt": "ロードマップ項目を状態ごとに数えた内訳です。{parts}。",
        "checklist": "完了した作業項目",
        "checklist_note": "全項目の進捗欄にあるチェックボックス {total} 件のうち {done} 件が完了しています",
        "search_label": "ロードマップ項目を検索",
        "search_placeholder": "ID、題名、トピック、状態で絞り込む",
        "filter_status": "状態",
        "filter_topic": "トピック",
        "view_label": "表示方法を選ぶ",
        "view_cards": "カード",
        "view_table": "表",
        "view_map": "関連図",
        "implemented_of": "{total}件中{done}件が実装済み",
        "items_count": "{count}件",
        "col_id": "ID",
        "col_title": "項目",
        "col_topic": "トピック",
        "col_status": "状態",
        "col_progress": "進捗",
        "col_updated": "更新日",
        "related": "関連",
        "progress_boxes": "作業項目 {total} 件中 {done} 件",
        "checklist_summary": "進捗チェックリスト",
        "map_caption": (
            "項目を識別子の順に左から並べ、トピックごとの行にまとめました。"
            "関連欄で互いを指している項目どうしを線で結んでいます。"
        ),
        "map_hint": "点にカーソルを合わせると題名を表示します。選ぶと項目の本文を開きます。",
        "map_alt": "ロードマップ項目をトピックごとの行に並べ、関連を線で結んだ図です。",
        "empty_query": "「{query}」に一致する項目はありません。",
        "empty_filters": "現在の絞り込み条件に一致する項目はありません。",
        "unknown_date": "—",
        "footer_source": "このページは次のリポジトリの roadmaps/ ディレクトリから生成しています。",
        "footer_readme": "ロードマップの読み方",
        "footer_requirements": "要件一覧",
        "updated_prefix": "ロードマップの最終更新",
    },
}


# --------------------------------------------------------------------------------------------
# Fragments
# --------------------------------------------------------------------------------------------


def item_url(item: Item, lang: str, repo: str, branch: str) -> str:
    suffix = "" if lang == "en" else "-ja"
    name = f"{item.id}-{item.slug}"
    return f"https://github.com/{repo}/blob/{branch}/roadmaps/{name}/{name}{suffix}.md"


def search_text(item: Item, lang: str) -> str:
    """The id, title, topic, and status a card, a row, and a map node all filter on."""
    doc = item.docs[lang]
    parts = [
        item.id,
        doc.title,
        TOPIC_BY_KEY[item.topic_key].label[lang],
        STATUS_BY_KEY[item.status_key].label[lang],
    ]
    return " ".join(parts).lower()


def stack_bar(counts: dict[str, int], total: int, lang: str, label: str, small: bool = False) -> str:
    """A stacked bar of a status composition.

    Segments sit in the fixed status order with a 2px surface gap between them, and each carries its
    count as a direct label once it is wide enough to hold one. The bar as a whole is one image with
    a written alternative, so a reader who never sees the colours still gets the numbers.
    """
    segments = []
    for status in STATUSES:
        count = counts.get(status.key, 0)
        if not count:
            continue
        share = 100 * count / total
        text = (
            f'<span class="seg-value">{count}</span>' if share >= 11 and not small else ""
        )
        segments.append(
            f'<span class="seg" data-status="{status.key}" style="flex-grow:{count}" '
            f'title="{esc(status.label[lang])}: {count}">{text}</span>'
        )
    classes = "stack stack-sm" if small else "stack"
    return f'<div class="{classes}" role="img" aria-label="{esc(label)}">{"".join(segments)}</div>'


def meter(done: int, total: int, label: str, small: bool = False) -> str:
    """A single-value meter: the share of checklist boxes ticked, on a recessive track."""
    share = pct(done, total)
    classes = "meter meter-sm" if small else "meter"
    return (
        f'<div class="{classes}" role="img" aria-label="{esc(label)}" title="{esc(label)}">'
        f'<span class="meter-fill" style="width:{share}%"></span>'
        "</div>"
    )


def status_legend(counts: dict[str, int], total: int, lang: str) -> str:
    entries = []
    for status in STATUSES:
        count = counts.get(status.key, 0)
        if not count:
            continue
        entries.append(
            f'<li class="legend-item"><span class="swatch" data-status="{status.key}"></span>'
            f'<span class="legend-name">{esc(status.label[lang])}</span>'
            f'<span class="legend-value">{count}</span>'
            f'<span class="legend-share">{pct(count, total)}%</span></li>'
        )
    return f'<ul class="legend">{"".join(entries)}</ul>'


def badge(item: Item, lang: str) -> str:
    status = STATUS_BY_KEY[item.status_key]
    return f'<span class="badge" data-status="{status.key}">{esc(status.label[lang])}</span>'


def card(item: Item, lang: str, urls: dict[str, str], strings: dict[str, str]) -> str:
    doc = item.docs[lang]
    topic = TOPIC_BY_KEY[item.topic_key]
    related = "".join(
        f'<a class="rel" href="{urls[rid]}">{rid}</a>' for rid in item.related if rid in urls
    )
    related_block = (
        f'<p class="card-rel"><span class="card-rel-label">{esc(strings["related"])}</span>{related}</p>'
        if related
        else ""
    )
    checklist = "".join(
        f'<li class="check" data-checked="{str(checked).lower()}">{esc(text)}</li>'
        for checked, text in doc.checklist
    )
    boxes = strings["progress_boxes"].format(done=item.done, total=item.total)
    details = (
        f'<details class="card-details"><summary>{esc(strings["checklist_summary"])}'
        f'<span class="card-boxes">{esc(boxes)}</span></summary>'
        f'<ol class="checklist">{checklist}</ol></details>'
        if checklist
        else ""
    )
    intro = f'<p class="card-intro">{esc(truncate(doc.intro, 240))}</p>' if doc.intro else ""
    return (
        f'<article class="card" data-id="{item.id}" data-status="{item.status_key}" '
        f'data-topic="{item.topic_key}" data-search="{esc(search_text(item, lang))}">'
        '<div class="card-top">'
        f'<span class="card-id">{item.id}</span>{badge(item, lang)}'
        "</div>"
        f'<h4 class="card-title"><a href="{urls[item.id]}">{esc(doc.title)}</a></h4>'
        f"{intro}"
        f'<div class="card-progress">{meter(item.done, item.total, boxes, small=True)}'
        f'<span class="card-progress-text">{esc(boxes)}</span></div>'
        f'<p class="card-meta"><span>{esc(topic.label[lang])}</span>'
        f'<span>{esc(item.docs[lang].author)}</span></p>'
        f"{related_block}{details}"
        "</article>"
    )


def cards_view(items: list[Item], lang: str, urls: dict[str, str], strings: dict[str, str]) -> str:
    sections = []
    for topic in TOPICS:
        group = [it for it in items if it.topic_key == topic.key]
        if not group:
            continue
        counts = {s.key: sum(1 for it in group if it.status_key == s.key) for s in STATUSES}
        implemented = counts["implemented"]
        share = pct(implemented, len(group))
        detail = strings["implemented_of"].format(done=implemented, total=len(group))
        alt = ", ".join(
            f"{STATUS_BY_KEY[key].label[lang]} {value}" for key, value in counts.items() if value
        )
        sections.append(
            f'<section class="topic" data-topic="{topic.key}">'
            '<header class="topic-head">'
            f'<h3 class="topic-name">{esc(topic.label[lang])}</h3>'
            f'<p class="topic-prog"><span class="topic-pct">{share}%</span>'
            f'<span class="topic-detail">{esc(detail)}</span></p>'
            f"{stack_bar(counts, len(group), lang, alt, small=True)}"
            "</header>"
            f'<div class="cards">{"".join(card(it, lang, urls, strings) for it in group)}</div>'
            "</section>"
        )
    return f'<div class="view view-cards">{"".join(sections)}</div>'


def table_view(items: list[Item], lang: str, urls: dict[str, str], strings: dict[str, str]) -> str:
    columns = (
        ("id", strings["col_id"]),
        ("title", strings["col_title"]),
        ("topic", strings["col_topic"]),
        ("status", strings["col_status"]),
        ("progress", strings["col_progress"]),
        ("updated", strings["col_updated"]),
    )
    heads = "".join(
        f'<th scope="col" data-sort-key="{key}" aria-sort="none" tabindex="0">{esc(label)}</th>'
        for key, label in columns
    )
    rows = []
    for item in items:
        doc = item.docs[lang]
        boxes = strings["progress_boxes"].format(done=item.done, total=item.total)
        updated = item.updated[:10] if item.updated else strings["unknown_date"]
        rows.append(
            f'<tr class="row" data-status="{item.status_key}" data-topic="{item.topic_key}" '
            f'data-search="{esc(search_text(item, lang))}">'
            f'<td class="cell-id" data-sort="{item.id}">{item.id}</td>'
            f'<th scope="row" class="cell-title"><a href="{urls[item.id]}">{esc(doc.title)}</a></th>'
            f"<td>{esc(TOPIC_BY_KEY[item.topic_key].label[lang])}</td>"
            f"<td>{badge(item, lang)}</td>"
            f'<td data-sort="{item.done / item.total if item.total else 0:.4f}">'
            f'<span class="cell-progress">{meter(item.done, item.total, boxes, small=True)}'
            f'<span class="cell-progress-text">{item.done}/{item.total}</span></span></td>'
            f'<td class="cell-date" data-sort="{esc(item.updated or "")}">{esc(updated)}</td>'
            "</tr>"
        )
    return (
        '<div class="view view-table is-hidden"><div class="table-scroll">'
        f'<table class="table"><thead><tr>{heads}</tr></thead>'
        f'<tbody>{"".join(rows)}</tbody></table></div></div>'
    )


# Map geometry. The horizontal position carries the identifier order and the row carries the topic,
# so both dimensions are readable without colour; the status hue on each node is reinforced by the
# legend beneath the figure and by the caption that hovering a node fills in.
MAP_W = 900
MAP_LEFT = 176
MAP_RIGHT = 872
MAP_TOP = 40
MAP_ROW_H = 92
MAP_NODE_R = 9


def map_view(items: list[Item], lang: str, urls: dict[str, str], strings: dict[str, str]) -> str:
    rows = [t for t in TOPICS if any(it.topic_key == t.key for it in items)]
    row_y = {topic.key: MAP_TOP + index * MAP_ROW_H + MAP_ROW_H / 2 for index, topic in enumerate(rows)}
    height = int(MAP_TOP + len(rows) * MAP_ROW_H + 34)
    step = (MAP_RIGHT - MAP_LEFT) / len(items)
    pos = {
        it.id: (MAP_LEFT + (it.order + 0.5) * step, row_y[it.topic_key])
        for it in items
    }

    guides = "".join(
        f'<g class="map-row"><line class="map-rule" x1="{MAP_LEFT - 14:.1f}" '
        f'y1="{row_y[topic.key]:.1f}" x2="{MAP_RIGHT:.1f}" y2="{row_y[topic.key]:.1f}"/>'
        f'<text class="map-row-label" x="{MAP_LEFT - 26}" y="{row_y[topic.key]:.1f}" '
        f'text-anchor="end" dominant-baseline="middle">{esc(topic.label[lang])}</text></g>'
        for topic in rows
    )

    seen: set[tuple[str, str]] = set()
    edges = []
    for item in items:
        for rid in item.related:
            pair = tuple(sorted((item.id, rid)))
            if pair in seen or rid not in pos:
                continue
            seen.add(pair)
            (x1, y1), (x2, y2) = pos[pair[0]], pos[pair[1]]
            if y1 == y2:
                # Same row: arc the line above the row so it never hides behind the rule.
                path = f"M{x1:.1f},{y1:.1f} Q{(x1 + x2) / 2:.1f},{y1 - 38:.1f} {x2:.1f},{y2:.1f}"
            else:
                bend = (x2 - x1) * 0.4
                path = (
                    f"M{x1:.1f},{y1:.1f} C{x1 + bend:.1f},{y1:.1f} "
                    f"{x2 - bend:.1f},{y2:.1f} {x2:.1f},{y2:.1f}"
                )
            edges.append(
                f'<path class="map-edge" d="{path}" data-a="{pair[0]}" data-b="{pair[1]}"/>'
            )

    nodes = []
    for item in items:
        x, y = pos[item.id]
        doc = item.docs[lang]
        caption = f"{item.id} — {doc.title}"
        nodes.append(
            f'<a class="map-node" href="{urls[item.id]}" data-id="{item.id}" '
            f'data-status="{item.status_key}" data-topic="{item.topic_key}" '
            f'data-search="{esc(search_text(item, lang))}" '
            f'data-caption="{esc(caption)}" '
            f'data-status-label="{esc(STATUS_BY_KEY[item.status_key].label[lang])}">'
            f"<title>{esc(caption)}</title>"
            f'<circle class="map-hit" cx="{x:.1f}" cy="{y:.1f}" r="{MAP_NODE_R + 12}"/>'
            f'<circle class="map-dot" cx="{x:.1f}" cy="{y:.1f}" r="{MAP_NODE_R}"/>'
            f'<text class="map-label" x="{x:.1f}" y="{y + MAP_NODE_R + 15:.1f}" '
            f'text-anchor="middle">{item.id}</text>'
            "</a>"
        )

    counts = {s.key: sum(1 for it in items if it.status_key == s.key) for s in STATUSES}
    return (
        '<div class="view view-map is-hidden">'
        f'<p class="map-caption">{esc(strings["map_caption"])}</p>'
        '<div class="map-scroll">'
        f'<svg class="map" viewBox="0 0 {MAP_W} {height}" role="img" '
        f'aria-label="{esc(strings["map_alt"])}" preserveAspectRatio="xMidYMid meet">'
        f'<g class="map-rows">{guides}</g>'
        f'<g class="map-edges">{"".join(edges)}</g>'
        f'<g class="map-nodes">{"".join(nodes)}</g>'
        "</svg></div>"
        f'<p class="map-readout" role="status" data-default="{esc(strings["map_hint"])}">'
        f'{esc(strings["map_hint"])}</p>'
        f'{status_legend(counts, len(items), lang)}'
        "</div>"
    )


def chips(name: str, entries: list[tuple[str, str, int]], legend: str, kind: str) -> str:
    """A row of filter checkboxes. Every box starts checked, so the page filters nothing until asked."""
    boxes = "".join(
        f'<label class="chip is-on"{f" data-status={key}" if kind == "status" else ""}>'
        f'<input type="checkbox" class="chip-box" data-filter="{kind}" data-key="{key}" checked>'
        f'<span class="chip-name">{esc(label)}</span>'
        f'<span class="chip-count">{count}</span></label>'
        for key, label, count in entries
    )
    return (
        f'<fieldset class="chips" data-kind="{kind}">'
        f'<legend class="chips-legend">{esc(legend)}</legend>{boxes}</fieldset>'
    )


# --------------------------------------------------------------------------------------------
# Page
# --------------------------------------------------------------------------------------------


def render_page(items: list[Item], lang: str, repo: str, branch: str) -> str:
    strings = STRINGS[lang]
    other = "ja" if lang == "en" else "en"
    prefix = "" if lang == "en" else "../"
    other_href = "ja/" if lang == "en" else "../"
    urls = {it.id: item_url(it, lang, repo, branch) for it in items}

    counts = {s.key: sum(1 for it in items if it.status_key == s.key) for s in STATUSES}
    topic_counts = {t.key: sum(1 for it in items if it.topic_key == t.key) for t in TOPICS}
    done = sum(it.done for it in items)
    boxes = sum(it.total for it in items)
    topics_present = [t for t in TOPICS if topic_counts[t.key]]
    updated = max((it.updated for it in items if it.updated), default=None)

    composition_parts = ", ".join(
        f"{STATUS_BY_KEY[key].label[lang]} {value}" for key, value in counts.items() if value
    )
    composition_alt = strings["composition_alt"].format(parts=composition_parts)
    checklist_note = strings["checklist_note"].format(done=done, total=boxes)

    head = (
        '<header class="site-head"><div class="wrap head-inner">'
        f'<a class="brand" href="{prefix or "./"}index.html">'
        f'<span class="brand-mark" aria-hidden="true"></span>'
        f'<span class="brand-name">{esc(strings["brand"])}</span></a>'
        '<nav class="head-nav">'
        f'<a class="head-link" href="https://github.com/{repo}">{esc(strings["github"])}</a>'
        f'<a class="head-link" href="{other_href}" lang="{STRINGS[other]["html_lang"]}" '
        f'hreflang="{STRINGS[other]["html_lang"]}">{esc(strings["other_lang"])}</a>'
        f'<button type="button" class="theme-btn" data-theme-toggle '
        f'aria-label="{esc(strings["theme"])}">'
        f'<span class="theme-name" data-auto="{esc(strings["theme_auto"])}" '
        f'data-light="{esc(strings["theme_light"])}" data-dark="{esc(strings["theme_dark"])}">'
        f'{esc(strings["theme_auto"])}</span></button>'
        "</nav></div></header>"
    )

    updated_line = (
        f'<p class="hero-updated">{esc(strings["updated_prefix"])} <time datetime="{updated}">'
        f"{updated[:10]}</time></p>"
        if updated
        else ""
    )
    hero = (
        '<section class="hero"><div class="wrap">'
        f'<p class="eyebrow">{esc(strings["eyebrow"])}</p>'
        f'<h1>{esc(strings["h1"])}</h1>'
        f'<p class="lede">{esc(strings["lede"])}</p>'
        f"{updated_line}"
        "</div></section>"
    )

    overview = (
        '<section class="overview" aria-labelledby="overview-h"><div class="wrap">'
        f'<h2 class="section-h" id="overview-h">{esc(strings["overview"])}</h2>'
        '<div class="overview-grid">'
        '<div class="panel hero-figure">'
        f'<span class="hero-value">{len(items)}</span>'
        f'<span class="hero-label">{esc(strings["hero_label"])}</span>'
        f'<span class="hero-sub">{esc(strings["hero_sub"].format(topics=len(topics_present)))}</span>'
        "</div>"
        '<figure class="panel chart">'
        f'<figcaption class="chart-title">{esc(strings["composition"])}</figcaption>'
        f"{stack_bar(counts, len(items), lang, composition_alt)}"
        f"{status_legend(counts, len(items), lang)}"
        "</figure>"
        '<figure class="panel chart">'
        f'<figcaption class="chart-title">{esc(strings["checklist"])}</figcaption>'
        f'<p class="chart-value">{pct(done, boxes)}<span class="chart-unit">%</span></p>'
        f"{meter(done, boxes, checklist_note)}"
        f'<p class="chart-note">{esc(checklist_note)}</p>'
        "</figure>"
        "</div></div></section>"
    )

    status_entries = [
        (s.key, s.label[lang], counts[s.key]) for s in STATUSES if counts[s.key]
    ]
    topic_entries = [(t.key, t.label[lang], topic_counts[t.key]) for t in topics_present]
    controls = (
        '<section class="controls" aria-label="{label}"><div class="wrap">'
        '<div class="control-row">'
        f'<label class="search-field"><span class="sr-only">{esc(strings["search_label"])}</span>'
        f'<input type="search" class="search" placeholder="{esc(strings["search_placeholder"])}">'
        "</label>"
        f'<div class="views" role="group" aria-label="{esc(strings["view_label"])}">'
        f'<button type="button" class="view-btn is-on" data-view="cards" aria-pressed="true">'
        f'{esc(strings["view_cards"])}</button>'
        f'<button type="button" class="view-btn" data-view="table" aria-pressed="false">'
        f'{esc(strings["view_table"])}</button>'
        f'<button type="button" class="view-btn" data-view="map" aria-pressed="false">'
        f'{esc(strings["view_map"])}</button>'
        "</div></div>"
        f'{chips("status", status_entries, strings["filter_status"], "status")}'
        f'{chips("topic", topic_entries, strings["filter_topic"], "topic")}'
        "</div></section>"
    ).format(label=esc(strings["search_label"]))

    views = (
        '<section class="board" id="board"><div class="wrap">'
        f"{cards_view(items, lang, urls, strings)}"
        f"{table_view(items, lang, urls, strings)}"
        f"{map_view(items, lang, urls, strings)}"
        f'<p class="empty" role="status" data-msg-query="{esc(strings["empty_query"])}" '
        f'data-msg-filters="{esc(strings["empty_filters"])}"></p>'
        "</div></section>"
    )

    readme = f"https://github.com/{repo}/blob/{branch}/roadmaps/README{'' if lang == 'en' else '-ja'}.md"
    requirements = (
        f"https://github.com/{repo}/blob/{branch}/docs/"
        f"{'requirements.md' if lang == 'en' else 'ja/requirements.md'}"
    )
    footer = (
        '<footer class="site-foot"><div class="wrap">'
        f'<p>{esc(strings["footer_source"])} '
        f'<a href="https://github.com/{repo}">{repo}</a></p>'
        '<p class="foot-links">'
        f'<a href="{readme}">{esc(strings["footer_readme"])}</a>'
        f'<a href="{requirements}">{esc(strings["footer_requirements"])}</a>'
        "</p></div></footer>"
    )

    return f"""<!doctype html>
<html lang="{strings["html_lang"]}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{esc(strings["title"])}</title>
<meta name="description" content="{esc(strings["description"])}">
<meta name="color-scheme" content="light dark">
<link rel="icon" href="{prefix}assets/favicon.svg" type="image/svg+xml">
<link rel="alternate" hreflang="{STRINGS[other]["html_lang"]}" href="{other_href}">
<link rel="stylesheet" href="{prefix}assets/style.css">
<script>try{{var t=localStorage.getItem('citywalk-roadmap-theme');\
if(t==='light'||t==='dark')document.documentElement.setAttribute('data-theme',t);}}catch(e){{}}</script>
</head>
<body>
<a class="skip" href="#board">{esc(strings["skip"])}</a>
{head}
<main>
{hero}
{overview}
{controls}
{views}
</main>
{footer}
<script src="{prefix}assets/app.js" defer></script>
</body>
</html>
"""


# --------------------------------------------------------------------------------------------
# Assets
# --------------------------------------------------------------------------------------------

FAVICON = """<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">
<rect width="32" height="32" rx="7" fill="#2a78d6"/>
<path d="M7 23c4.5 0 4.5-6 9-6s4.5-6 9-6" fill="none" stroke="#fcfcfb" stroke-width="2.6"
 stroke-linecap="round"/>
<circle cx="7" cy="23" r="2.6" fill="#fcfcfb"/>
<circle cx="25" cy="11" r="2.6" fill="#fcfcfb"/>
</svg>
"""

STYLE = r"""/* citywalk roadmap — generated by scripts/build_roadmap_site.py, do not edit by hand. */

:root {
  color-scheme: light;
  --surface: #fcfcfb;
  --plane: #f9f9f7;
  --ink: #0b0b0b;
  --ink-2: #52514e;
  --muted: #898781;
  --grid: #e1e0d9;
  --rule: #c3c2b7;
  --border: rgba(11, 11, 11, 0.10);
  --track: rgba(11, 11, 11, 0.08);
  --wash: rgba(11, 11, 11, 0.04);
  --accent: #2a78d6;
  --st-implemented: #008300;
  --st-in-progress: #eda100;
  --st-proposal: #2a78d6;
  --st-deferred: #e87ba4;
  --sans: system-ui, -apple-system, "Segoe UI", "Hiragino Sans", "Noto Sans JP",
    "Yu Gothic UI", sans-serif;
}

@media (prefers-color-scheme: dark) {
  :root:where(:not([data-theme="light"])) {
    color-scheme: dark;
    --surface: #1a1a19;
    --plane: #0d0d0d;
    --ink: #ffffff;
    --ink-2: #c3c2b7;
    --muted: #898781;
    --grid: #2c2c2a;
    --rule: #383835;
    --border: rgba(255, 255, 255, 0.10);
    --track: rgba(255, 255, 255, 0.10);
    --wash: rgba(255, 255, 255, 0.06);
    --accent: #3987e5;
    --st-implemented: #008300;
    --st-in-progress: #c98500;
    --st-proposal: #3987e5;
    --st-deferred: #d55181;
  }
}

:root[data-theme="dark"] {
  color-scheme: dark;
  --surface: #1a1a19;
  --plane: #0d0d0d;
  --ink: #ffffff;
  --ink-2: #c3c2b7;
  --muted: #898781;
  --grid: #2c2c2a;
  --rule: #383835;
  --border: rgba(255, 255, 255, 0.10);
  --track: rgba(255, 255, 255, 0.10);
  --wash: rgba(255, 255, 255, 0.06);
  --accent: #3987e5;
  --st-implemented: #008300;
  --st-in-progress: #c98500;
  --st-proposal: #3987e5;
  --st-deferred: #d55181;
}

*, *::before, *::after { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--plane);
  color: var(--ink);
  font-family: var(--sans);
  font-size: 15px;
  line-height: 1.6;
  -webkit-text-size-adjust: 100%;
}

a { color: inherit; }
h1, h2, h3, h4 { line-height: 1.3; }

.wrap { width: min(1120px, 100% - 3rem); margin-inline: auto; }
@media (max-width: 640px) { .wrap { width: min(1120px, 100% - 2rem); } }

.sr-only {
  position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px;
  overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0;
}

.skip {
  position: absolute; left: -9999px; top: 0; z-index: 10;
  background: var(--surface); color: var(--ink); padding: .6rem 1rem; border: 1px solid var(--border);
}
.skip:focus { left: .75rem; top: .75rem; }

/* --- masthead --- */

.site-head {
  position: sticky; top: 0; z-index: 5;
  background: color-mix(in srgb, var(--plane) 88%, transparent);
  backdrop-filter: blur(8px);
  border-bottom: 1px solid var(--border);
}
.head-inner { display: flex; align-items: center; justify-content: space-between; gap: 1rem;
  min-height: 56px; }
.brand { display: inline-flex; align-items: center; gap: .55rem; text-decoration: none;
  font-weight: 600; letter-spacing: -0.01em; }
.brand-mark {
  width: 18px; height: 18px; border-radius: 5px; background: var(--accent);
  box-shadow: inset 0 0 0 3px var(--plane), 0 0 0 1px var(--accent);
}
.head-nav { display: flex; align-items: center; gap: .35rem; }
.head-link, .theme-btn {
  font: inherit; font-size: 13px; color: var(--ink-2); text-decoration: none;
  padding: .3rem .6rem; border-radius: 7px; border: 1px solid transparent; background: none;
  cursor: pointer;
}
.head-link:hover, .theme-btn:hover { background: var(--wash); color: var(--ink); }
.theme-btn { border-color: var(--border); }

/* --- hero --- */

.hero { padding: 3rem 0 1.5rem; }
.eyebrow { margin: 0; font-size: 12px; letter-spacing: .08em; text-transform: uppercase;
  color: var(--muted); }
.hero h1 { margin: .2rem 0 .6rem; font-size: clamp(2rem, 5vw, 2.75rem); letter-spacing: -0.02em; }
.lede { margin: 0; max-width: 62ch; color: var(--ink-2); }
.hero-updated { margin: 1rem 0 0; font-size: 12.5px; color: var(--muted); }

/* --- panels --- */

.section-h {
  margin: 0 0 .9rem; font-size: 12px; font-weight: 600; letter-spacing: .08em;
  text-transform: uppercase; color: var(--muted);
}
.overview { padding: 1.5rem 0; }
.overview-grid {
  display: grid; gap: .9rem;
  grid-template-columns: repeat(auto-fit, minmax(248px, 1fr));
}
.panel {
  margin: 0; padding: 1.1rem 1.2rem; background: var(--surface);
  border: 1px solid var(--border); border-radius: 12px;
}
.hero-figure { display: flex; flex-direction: column; justify-content: center; }
.hero-value { font-size: 3.4rem; font-weight: 600; line-height: 1; letter-spacing: -0.03em; }
.hero-label { margin-top: .45rem; font-size: 14px; color: var(--ink-2); }
.hero-sub { font-size: 12.5px; color: var(--muted); }
.chart-title { font-size: 12.5px; font-weight: 600; color: var(--ink-2); margin-bottom: .7rem; }
.chart-value { margin: 0 0 .55rem; font-size: 2.1rem; font-weight: 600; line-height: 1;
  letter-spacing: -0.02em; }
.chart-unit { font-size: 1rem; font-weight: 500; color: var(--muted); margin-left: .1rem; }
.chart-note { margin: .6rem 0 0; font-size: 12px; color: var(--muted); }

/* --- marks --- */

.stack { display: flex; gap: 2px; height: 14px; }
.stack-sm { height: 7px; max-width: 320px; }
.seg { position: relative; display: flex; align-items: center; justify-content: center;
  min-width: 3px; border-radius: 2px; background: var(--muted); }
.stack .seg:first-child { border-start-start-radius: 4px; border-end-start-radius: 4px; }
.stack .seg:last-child { border-start-end-radius: 4px; border-end-end-radius: 4px; }
.seg-value { font-size: 10px; font-weight: 600; color: var(--surface); }
.seg[data-status="implemented"] { background: var(--st-implemented); }
.seg[data-status="in-progress"] { background: var(--st-in-progress); }
.seg[data-status="proposal"] { background: var(--st-proposal); }
.seg[data-status="deferred"] { background: var(--st-deferred); }

.meter { height: 8px; border-radius: 4px; background: var(--track); overflow: hidden; }
.meter-sm { height: 5px; }
.meter-fill { display: block; height: 100%; border-radius: 4px;
  background: var(--st-implemented); min-width: 0; }

.legend { list-style: none; display: flex; flex-wrap: wrap; gap: .35rem 1rem;
  margin: .8rem 0 0; padding: 0; font-size: 12px; color: var(--ink-2); }
.legend-item { display: inline-flex; align-items: center; gap: .35rem; }
.swatch { width: 9px; height: 9px; border-radius: 3px; background: var(--muted); flex: none; }
.swatch[data-status="implemented"] { background: var(--st-implemented); }
.swatch[data-status="in-progress"] { background: var(--st-in-progress); }
.swatch[data-status="proposal"] { background: var(--st-proposal); }
.swatch[data-status="deferred"] { background: var(--st-deferred); }
.legend-value { font-weight: 600; font-variant-numeric: tabular-nums; }
.legend-share { color: var(--muted); font-variant-numeric: tabular-nums; }

.badge {
  display: inline-block; font-size: 11px; line-height: 1.5; padding: 0 .4rem;
  border-radius: 5px; border: 1px solid currentColor; white-space: nowrap;
}
.badge[data-status="implemented"] { color: var(--st-implemented); }
.badge[data-status="in-progress"] { color: var(--st-in-progress); }
.badge[data-status="proposal"] { color: var(--st-proposal); }
.badge[data-status="deferred"] { color: var(--st-deferred); }

/* --- controls --- */

.controls { padding: 1rem 0 .5rem; }
.control-row { display: flex; flex-wrap: wrap; align-items: center; gap: .75rem;
  margin-bottom: .8rem; }
.search-field { flex: 1 1 260px; }
.search {
  width: 100%; font: inherit; font-size: 14px; padding: .45rem .75rem;
  border: 1px solid var(--border); border-radius: 9px; background: var(--surface); color: inherit;
}
.search:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.views { display: inline-flex; border: 1px solid var(--border); border-radius: 9px;
  overflow: hidden; background: var(--surface); }
.view-btn { font: inherit; font-size: 13px; padding: .4rem .95rem; border: 0; background: none;
  color: var(--ink-2); cursor: pointer; }
.view-btn + .view-btn { border-left: 1px solid var(--border); }
.view-btn.is-on { background: var(--wash); color: var(--ink); font-weight: 600; }

.chips { display: flex; flex-wrap: wrap; align-items: center; gap: .4rem;
  border: 0; margin: 0 0 .5rem; padding: 0; }
.chips-legend { padding: 0; margin-right: .3rem; font-size: 12px; color: var(--muted);
  min-width: 4.5rem; }
.chip {
  display: inline-flex; align-items: center; gap: .4rem; cursor: pointer; user-select: none;
  font-size: 12.5px; padding: .18rem .55rem; border-radius: 7px;
  border: 1px solid var(--border); background: var(--surface); opacity: .45;
}
.chip.is-on { opacity: 1; }
.chip-box { width: 13px; height: 13px; margin: 0; accent-color: var(--accent); flex: none; }
.chip[data-status="implemented"] .chip-box { accent-color: var(--st-implemented); }
.chip[data-status="in-progress"] .chip-box { accent-color: var(--st-in-progress); }
.chip[data-status="proposal"] .chip-box { accent-color: var(--st-proposal); }
.chip[data-status="deferred"] .chip-box { accent-color: var(--st-deferred); }
.chip-count { font-variant-numeric: tabular-nums; color: var(--muted); }

/* --- board --- */

.board { padding: .5rem 0 3rem; }
.is-hidden { display: none !important; }

.topic { margin: 1.6rem 0 2rem; }
.topic-head { margin-bottom: .75rem; }
.topic-name { margin: 0; font-size: 17px; letter-spacing: -0.01em; }
.topic-prog { display: flex; align-items: baseline; gap: .5rem; margin: .15rem 0 .4rem; }
.topic-pct { font-size: 14px; font-weight: 600; font-variant-numeric: tabular-nums; }
.topic-detail { font-size: 12px; color: var(--muted); }

.cards { display: grid; gap: .7rem;
  grid-template-columns: repeat(auto-fill, minmax(268px, 1fr)); }
.card {
  display: flex; flex-direction: column; gap: .45rem; padding: .85rem .95rem;
  background: var(--surface); border: 1px solid var(--border); border-radius: 11px;
}
.card:hover { border-color: var(--rule); }
.card-top { display: flex; align-items: center; justify-content: space-between; gap: .5rem; }
.card-id { font-size: 12px; font-weight: 600; color: var(--muted);
  font-variant-numeric: tabular-nums; }
.card-title { margin: 0; font-size: 14.5px; }
.card-title a { text-decoration: none; }
.card-title a:hover { text-decoration: underline; }
.card-intro { margin: 0; font-size: 12.5px; line-height: 1.55; color: var(--ink-2); }
.card-progress { display: flex; align-items: center; gap: .5rem; }
.card-progress .meter { flex: 1; }
.card-progress-text { font-size: 11.5px; color: var(--muted); white-space: nowrap; }
.card-meta, .card-rel { display: flex; flex-wrap: wrap; align-items: center; gap: .5rem;
  margin: 0; font-size: 11.5px; color: var(--muted); }
.card-meta > span + span::before,
.card-rel-label + .rel::before { content: "·"; margin-right: .5rem; color: var(--rule); }
.card-rel-label + .rel::before { content: none; }
.rel { font-variant-numeric: tabular-nums; text-decoration: none;
  border-bottom: 1px solid var(--border); }
.rel:hover { border-color: currentColor; color: var(--ink); }
.card-details { font-size: 12px; }
.card-details > summary { cursor: pointer; color: var(--ink-2); display: flex;
  align-items: baseline; gap: .4rem; }
.card-boxes { color: var(--muted); font-variant-numeric: tabular-nums; }
.checklist { margin: .5rem 0 0; padding-left: 1.1rem; color: var(--ink-2); }
.checklist .check { margin-bottom: .3rem; line-height: 1.5; }
.checklist .check[data-checked="true"] { color: var(--muted); text-decoration: line-through; }

/* --- table --- */

.table-scroll { overflow-x: auto; border: 1px solid var(--border); border-radius: 11px;
  background: var(--surface); }
.table { width: 100%; min-width: 720px; border-collapse: collapse; font-size: 13.5px; }
.table th, .table td { text-align: left; padding: .55rem .8rem;
  border-bottom: 1px solid var(--grid); vertical-align: middle; }
.table thead th { font-size: 12px; color: var(--ink-2); white-space: nowrap;
  cursor: pointer; user-select: none; background: var(--surface); position: sticky; top: 0; }
.table thead th:hover { background: var(--wash); }
.table thead th[aria-sort="ascending"]::after { content: " ▲"; font-size: 8px; }
.table thead th[aria-sort="descending"]::after { content: " ▼"; font-size: 8px; }
.table tbody tr:hover { background: var(--wash); }
.table tbody tr:last-child td, .table tbody tr:last-child th { border-bottom: 0; }
.cell-id { font-variant-numeric: tabular-nums; color: var(--muted); white-space: nowrap; }
.cell-title { font-weight: 500; }
.cell-title a { text-decoration: none; }
.cell-title a:hover { text-decoration: underline; }
.cell-progress { display: flex; align-items: center; gap: .5rem; min-width: 118px; }
.cell-progress .meter { flex: 1; min-width: 56px; }
.cell-progress-text { font-size: 11.5px; color: var(--muted);
  font-variant-numeric: tabular-nums; }
.cell-date { color: var(--muted); white-space: nowrap; font-variant-numeric: tabular-nums; }

/* --- map --- */

.map-caption { margin: 0 0 .9rem; max-width: 64ch; font-size: 12.5px; color: var(--ink-2); }
.map-scroll { overflow-x: auto; background: var(--surface); border: 1px solid var(--border);
  border-radius: 11px; padding: .5rem; }
.map { display: block; width: 100%; min-width: 720px; height: auto; }
.map-rule { stroke: var(--grid); stroke-width: 1; }
.map-row-label { fill: var(--ink-2); font-size: 12px; font-family: var(--sans); }
.map-edge { fill: none; stroke: var(--rule); stroke-width: 1.5; opacity: .55; }
.map-node { cursor: pointer; }
.map-hit { fill: transparent; }
.map-dot { fill: var(--muted); stroke: var(--surface); stroke-width: 2; }
.map-node[data-status="implemented"] .map-dot { fill: var(--st-implemented); }
.map-node[data-status="in-progress"] .map-dot { fill: var(--st-in-progress); }
.map-node[data-status="proposal"] .map-dot { fill: var(--st-proposal); }
.map-node[data-status="deferred"] .map-dot { fill: var(--st-deferred); }
.map-label { fill: var(--muted); font-size: 9.5px; font-family: var(--sans);
  font-variant-numeric: tabular-nums; }
.map-node:hover .map-dot, .map-node:focus-visible .map-dot { stroke: var(--ink); }
.map-node:hover .map-label, .map-node:focus-visible .map-label { fill: var(--ink); }
.map.is-picking .map-node:not(.is-lit) { opacity: .28; }
.map.is-picking .map-edge:not(.is-lit) { opacity: .12; }
.map-edge.is-lit { stroke: var(--accent); opacity: 1; stroke-width: 2; }
.map-node.is-out, .map-edge.is-out { display: none; }
.map-readout { margin: .8rem 0 0; font-size: 12.5px; color: var(--ink-2); min-height: 1.5em; }
.view-map .legend { margin-top: .6rem; }

.empty { margin: 1.5rem 0; font-size: 13.5px; color: var(--ink-2); }
.empty:empty { margin: 0; }

/* --- footer --- */

.site-foot { border-top: 1px solid var(--border); padding: 1.6rem 0 2.5rem;
  font-size: 12.5px; color: var(--muted); }
.site-foot p { margin: 0 0 .35rem; }
.foot-links { display: flex; flex-wrap: wrap; gap: 1rem; }
.foot-links a { color: var(--ink-2); }

@media (prefers-reduced-motion: reduce) {
  * { transition: none !important; animation: none !important; }
}
"""

SCRIPT = r"""/* citywalk roadmap — generated by scripts/build_roadmap_site.py, do not edit by hand.
   Progressive enhancement throughout: with scripting off every filter is on, the cards view is
   shown, and the page stays fully readable. Nothing here fetches or recomputes data — the script
   only shows, hides, and reorders markup the build already rendered. */
(function () {
  'use strict';

  var root = document.documentElement;
  var THEME_KEY = 'citywalk-roadmap-theme';
  var VIEW_KEY = 'citywalk-roadmap-view';

  function store(key, value) {
    try { localStorage.setItem(key, value); } catch (e) { /* private mode: ignore */ }
  }
  function recall(key) {
    try { return localStorage.getItem(key); } catch (e) { return null; }
  }

  /* --- theme: auto -> light -> dark, remembered across visits --- */

  var themeBtn = document.querySelector('[data-theme-toggle]');
  if (themeBtn) {
    var name = themeBtn.querySelector('.theme-name');
    var order = ['auto', 'light', 'dark'];
    function paint(mode) {
      if (mode === 'auto') { root.removeAttribute('data-theme'); }
      else { root.setAttribute('data-theme', mode); }
      if (name) { name.textContent = name.getAttribute('data-' + mode) || mode; }
    }
    var current = recall(THEME_KEY);
    if (order.indexOf(current) < 0) { current = 'auto'; }
    paint(current);
    themeBtn.addEventListener('click', function () {
      current = order[(order.indexOf(current) + 1) % order.length];
      paint(current);
      if (current === 'auto') { store(THEME_KEY, 'auto'); } else { store(THEME_KEY, current); }
    });
  }

  /* --- filters: status chips AND topic chips AND a free-text query --- */

  var search = document.querySelector('.search');
  var boxes = Array.prototype.slice.call(document.querySelectorAll('.chip-box'));
  var cards = Array.prototype.slice.call(document.querySelectorAll('.card'));
  var rows = Array.prototype.slice.call(document.querySelectorAll('.row'));
  var nodes = Array.prototype.slice.call(document.querySelectorAll('.map-node'));
  var edges = Array.prototype.slice.call(document.querySelectorAll('.map-edge'));
  var topics = Array.prototype.slice.call(document.querySelectorAll('.topic'));
  var empty = document.querySelector('.empty');

  var on = { status: {}, topic: {} };
  boxes.forEach(function (box) {
    on[box.getAttribute('data-filter')][box.getAttribute('data-key')] = box.checked;
  });

  function terms() {
    return (search ? search.value : '').toLowerCase().split(/\s+/).filter(Boolean);
  }

  function passes(element, query) {
    var hay = element.getAttribute('data-search') || '';
    if (!on.status[element.getAttribute('data-status')]) { return false; }
    if (!on.topic[element.getAttribute('data-topic')]) { return false; }
    return query.every(function (term) { return hay.indexOf(term) >= 0; });
  }

  function apply() {
    var query = terms();
    var shown = 0;
    var live = {};

    cards.forEach(function (card) {
      var visible = passes(card, query);
      if (visible) { shown += 1; }
      card.classList.toggle('is-hidden', !visible);
    });
    rows.forEach(function (row) { row.classList.toggle('is-hidden', !passes(row, query)); });
    nodes.forEach(function (node) {
      var visible = passes(node, query);
      live[node.getAttribute('data-id')] = visible;
      node.classList.toggle('is-out', !visible);
    });
    edges.forEach(function (edge) {
      var both = live[edge.getAttribute('data-a')] && live[edge.getAttribute('data-b')];
      edge.classList.toggle('is-out', !both);
    });
    topics.forEach(function (topic) {
      topic.classList.toggle('is-hidden', !topic.querySelector('.card:not(.is-hidden)'));
    });
    boxes.forEach(function (box) {
      box.closest('.chip').classList.toggle('is-on', box.checked);
    });

    if (empty) {
      if (shown > 0) {
        empty.textContent = '';
      } else if (query.length) {
        empty.textContent = (empty.getAttribute('data-msg-query') || '')
          .replace('{query}', search.value.trim());
      } else {
        empty.textContent = empty.getAttribute('data-msg-filters') || '';
      }
    }
  }

  boxes.forEach(function (box) {
    box.addEventListener('change', function () {
      on[box.getAttribute('data-filter')][box.getAttribute('data-key')] = box.checked;
      apply();
    });
  });
  if (search) { search.addEventListener('input', apply); }

  /* --- view toggle: one rendered view shown, the others hidden --- */

  var viewBtns = Array.prototype.slice.call(document.querySelectorAll('.view-btn'));
  var views = {
    cards: document.querySelector('.view-cards'),
    table: document.querySelector('.view-table'),
    map: document.querySelector('.view-map')
  };
  function setView(wanted) {
    Object.keys(views).forEach(function (key) {
      if (views[key]) { views[key].classList.toggle('is-hidden', key !== wanted); }
    });
    viewBtns.forEach(function (btn) {
      var active = btn.getAttribute('data-view') === wanted;
      btn.classList.toggle('is-on', active);
      btn.setAttribute('aria-pressed', String(active));
    });
    store(VIEW_KEY, wanted);
  }
  viewBtns.forEach(function (btn) {
    btn.addEventListener('click', function () { setView(btn.getAttribute('data-view')); });
  });
  var saved = recall(VIEW_KEY);
  setView(views[saved] ? saved : 'cards');

  /* --- table sort: reorder rendered rows, never change which ones the filters show --- */

  var table = document.querySelector('.table');
  var tbody = table ? table.querySelector('tbody') : null;
  var heads = table ? Array.prototype.slice.call(table.querySelectorAll('th[data-sort-key]')) : [];
  var sorted = null;
  var direction = 1;

  function value(row, index) {
    var cell = row.children[index];
    if (!cell) { return ''; }
    var explicit = cell.getAttribute('data-sort');
    return (explicit !== null ? explicit : cell.textContent || '').trim().toLowerCase();
  }

  heads.forEach(function (head, index) {
    function sortBy() {
      direction = sorted === index ? -direction : 1;
      sorted = index;
      Array.prototype.slice.call(tbody.children).sort(function (a, b) {
        var left = value(a, index);
        var right = value(b, index);
        if (left === right) { return 0; }
        return left < right ? -direction : direction;
      }).forEach(function (row) { tbody.appendChild(row); });
      heads.forEach(function (other) { other.setAttribute('aria-sort', 'none'); });
      head.setAttribute('aria-sort', direction > 0 ? 'ascending' : 'descending');
    }
    head.addEventListener('click', sortBy);
    head.addEventListener('keydown', function (event) {
      if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); sortBy(); }
    });
  });

  /* --- map hover: name the item under the pointer and light up what it relates to --- */

  var map = document.querySelector('.map');
  var readout = document.querySelector('.map-readout');
  var fallback = readout ? readout.getAttribute('data-default') || '' : '';

  function pick(node) {
    if (!map) { return; }
    var id = node.getAttribute('data-id');
    var lit = {};
    lit[id] = true;
    edges.forEach(function (edge) {
      var a = edge.getAttribute('data-a');
      var b = edge.getAttribute('data-b');
      var touches = (a === id || b === id) && !edge.classList.contains('is-out');
      edge.classList.toggle('is-lit', touches);
      if (touches) { lit[a] = true; lit[b] = true; }
    });
    nodes.forEach(function (other) {
      other.classList.toggle('is-lit', !!lit[other.getAttribute('data-id')]);
    });
    map.classList.add('is-picking');
    if (readout) {
      readout.textContent = node.getAttribute('data-caption') + ' · '
        + node.getAttribute('data-status-label');
    }
  }

  function release() {
    if (!map) { return; }
    map.classList.remove('is-picking');
    edges.forEach(function (edge) { edge.classList.remove('is-lit'); });
    nodes.forEach(function (node) { node.classList.remove('is-lit'); });
    if (readout) { readout.textContent = fallback; }
  }

  nodes.forEach(function (node) {
    node.addEventListener('mouseenter', function () { pick(node); });
    node.addEventListener('focus', function () { pick(node); });
    node.addEventListener('mouseleave', release);
    node.addEventListener('blur', release);
  });

  apply();
})();
"""


# --------------------------------------------------------------------------------------------
# Entry point
# --------------------------------------------------------------------------------------------


def data_feed(items: list[Item], repo: str, branch: str) -> str:
    """The same facts the pages show, as JSON, for anything that wants the roadmap as data."""
    payload = {
        "repository": repo,
        "branch": branch,
        "statuses": [
            {"key": s.key, "label": s.label, "color": {"light": s.light, "dark": s.dark}}
            for s in STATUSES
        ],
        "topics": [{"key": t.key, "label": t.label} for t in TOPICS],
        "items": [
            {
                "id": item.id,
                "slug": item.slug,
                "status": item.status_key,
                "topic": item.topic_key,
                "title": {lang: item.docs[lang].title for lang in LANGS},
                "author": item.docs["en"].author,
                "related": item.related,
                "progress": {"done": item.done, "total": item.total},
                "created": item.created,
                "updated": item.updated,
                "url": {lang: item_url(item, lang, repo, branch) for lang in LANGS},
            }
            for item in items
        ],
    }
    return json.dumps(payload, ensure_ascii=False, indent=2) + "\n"


def build(out: Path, items: list[Item], repo: str, branch: str) -> None:
    (out / "assets").mkdir(parents=True, exist_ok=True)
    (out / "ja").mkdir(parents=True, exist_ok=True)
    (out / "assets" / "style.css").write_text(STYLE, encoding="utf-8")
    (out / "assets" / "app.js").write_text(SCRIPT, encoding="utf-8")
    (out / "assets" / "favicon.svg").write_text(FAVICON, encoding="utf-8")
    (out / "index.html").write_text(render_page(items, "en", repo, branch), encoding="utf-8")
    (out / "ja" / "index.html").write_text(render_page(items, "ja", repo, branch), encoding="utf-8")
    (out / "roadmap.json").write_text(data_feed(items, repo, branch), encoding="utf-8")
    # GitHub Pages runs Jekyll unless told otherwise, and Jekyll drops paths it does not recognise.
    (out / ".nojekyll").write_text("", encoding="utf-8")


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT, help="output directory")
    parser.add_argument("--roadmaps", type=Path, default=ROADMAPS, help="roadmap source directory")
    parser.add_argument("--repo", default=DEFAULT_REPO, help="owner/name the item links point at")
    parser.add_argument("--branch", default=DEFAULT_BRANCH, help="branch the item links point at")
    parser.add_argument(
        "--no-git", action="store_true", help="skip the per-item Git date lookup"
    )
    args = parser.parse_args(argv)

    try:
        items = load_items(args.roadmaps, with_dates=not args.no_git)
    except BuildError as error:
        print(f"error: {error}", file=sys.stderr)
        return 1

    build(args.out, items, args.repo, args.branch)
    print(f"wrote {args.out} ({len(items)} items, {len(LANGS)} languages)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
