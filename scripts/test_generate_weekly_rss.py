import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
from datetime import datetime, timezone
from unittest import mock


SCRIPT = pathlib.Path(__file__).with_name("generate-weekly-rss.py")
SPEC = importlib.util.spec_from_file_location("generate_weekly_rss", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
generator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(generator)


class WeeklyRSSGeneratorTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temporary.name)
        self.feed = self.root / "feeds" / "microsoft-azure-updates"
        self.feed.mkdir(parents=True)
        (self.feed / "feed.json").write_text(
            json.dumps(
                {
                    "id": "microsoft-azure-updates",
                    "url": "https://example.microsoft.com/feed",
                }
            ),
            encoding="utf-8",
        )
        (self.feed / "weeks").mkdir()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def add_entry(
        self,
        entry_key: str,
        revision: str,
        published: datetime,
        *,
        content: str | None = None,
        enriched: str | None = None,
        title: str = "New Azure capability",
    ) -> pathlib.Path:
        iso = published.isocalendar()
        entry = self.feed / "weeks" / f"{iso.year}-W{iso.week:02d}" / entry_key
        (entry / "revisions").mkdir(parents=True)
        (entry / "index.json").write_text(
            json.dumps(
                {
                    "entry_key": entry_key,
                    "current_revision": revision,
                }
            ),
            encoding="utf-8",
        )
        revision_document = {
            "title": title,
            "url": f"https://azure.microsoft.com/updates?id={entry_key}",
            "published_at": int(published.timestamp()),
            "summary": "Feed summary.",
        }
        if content is not None:
            revision_document["content"] = content
        (entry / "revisions" / f"{revision}.json").write_text(
            json.dumps(revision_document), encoding="utf-8"
        )
        if enriched is not None:
            artifact = "f" * 64
            (entry / "enrichments").mkdir()
            (entry / "enrichment.json").write_text(
                json.dumps(
                    {
                        "current_artifact": artifact,
                    }
                ),
                encoding="utf-8",
            )
            (entry / "enrichments" / f"{artifact}.json").write_text(
                json.dumps(
                    {
                        "entry_revision": revision,
                        "content": enriched,
                    }
                ),
                encoding="utf-8",
            )
        return entry

    def test_collects_only_current_iso_week_and_prefers_matching_enrichment(
        self,
    ) -> None:
        current = self.add_entry(
            "current",
            "a" * 64,
            datetime(2026, 11, 18, 12, tzinfo=timezone.utc),
            content="Original feed content.",
            enriched="Complete provider content.",
        )
        self.add_entry(
            "previous",
            "b" * 64,
            datetime(2026, 11, 15, 23, 59, tzinfo=timezone.utc),
            content="Previous week.",
        )

        feed_id, announcements = generator.collect_announcements(
            self.root, "2026-W47"
        )

        self.assertEqual(feed_id, "microsoft-azure-updates")
        self.assertEqual(len(announcements), 1)
        self.assertEqual(announcements[0].entry_key, "current")
        self.assertEqual(announcements[0].source_text, "Complete provider content.")
        self.assertTrue((current / "index.json").is_file())

    def test_rejects_an_iso_week_that_does_not_exist_in_the_year(self) -> None:
        with self.assertRaisesRegex(
            generator.InputError, "--week is not a valid ISO week"
        ):
            generator.parse_week("2021-W53")

    def test_ignores_an_enrichment_for_a_stale_revision(self) -> None:
        entry = self.add_entry(
            "stale",
            "c" * 64,
            datetime(2026, 11, 20, 12, tzinfo=timezone.utc),
            content="Current revision content.",
            enriched="Stale provider content.",
        )
        artifact = json.loads((entry / "enrichment.json").read_text())[
            "current_artifact"
        ]
        artifact_path = entry / "enrichments" / f"{artifact}.json"
        artifact_document = json.loads(artifact_path.read_text())
        artifact_document["entry_revision"] = "d" * 64
        artifact_path.write_text(json.dumps(artifact_document), encoding="utf-8")

        _, announcements = generator.collect_announcements(
            self.root, "2026-W47"
        )

        self.assertEqual(announcements[0].source_text, "Current revision content.")

    def test_skips_current_revisions_without_numeric_epoch_dates(self) -> None:
        self.add_entry(
            "without-date",
            "e" * 64,
            datetime(2026, 11, 20, 12, tzinfo=timezone.utc),
        )
        revision_path = (
            self.feed
            / "weeks"
            / "2026-W47"
            / "without-date"
            / "revisions"
            / f"{'e' * 64}.json"
        )
        revision = json.loads(revision_path.read_text())
        revision["published_at"] = "not-an-epoch"
        revision_path.write_text(json.dumps(revision), encoding="utf-8")

        _, announcements = generator.collect_announcements(
            self.root, "2026-W47"
        )

        self.assertEqual(announcements, [])

    def test_renders_all_summaries_before_the_introduction(self) -> None:
        announcement = generator.Announcement(
            entry_key="entry",
            title="A [new] capability",
            url="https://example.com/update?id=42",
            published_at=1,
            source_text="Supported source text.",
        )
        second = generator.Announcement(
            entry_key="second",
            title="Another capability",
            url="https://example.com/update?id=43",
            published_at=2,
            source_text="More supported source text.",
        )
        events = []

        def summarize(value: generator.Announcement) -> str:
            events.append(("summary", value.entry_key))
            return f"Summary for {value.entry_key}."

        def introduce(
            feed_name: str,
            week: str,
            updates: list[generator.SummarizedAnnouncement],
        ) -> str:
            events.append(
                (
                    "introduction",
                    [update.announcement.entry_key for update in updates],
                )
            )
            self.assertEqual(feed_name, "Microsoft Azure Updates")
            self.assertEqual(week, "2026-W47")
            return "AI and infrastructure feature prominently in this week's updates."

        report = generator.render_report(
            "microsoft-azure-updates",
            "2026-W47",
            [announcement, second],
            summarize,
            introduce,
        )

        self.assertEqual(
            events,
            [
                ("summary", "entry"),
                ("summary", "second"),
                ("introduction", ["entry", "second"]),
            ],
        )
        self.assertIn(
            "* [A \\[new\\] capability](https://example.com/update?id=42). - "
            "Summary for entry.",
            report,
        )
        self.assertTrue(report.startswith("This week featured 2 updates"))
        self.assertLess(
            report.index("AI and infrastructure feature"),
            report.index("Detailed list:"),
        )

    def test_empty_report_does_not_request_an_introduction(self) -> None:
        introduce = mock.Mock(
            side_effect=AssertionError("introduction should not be requested")
        )

        report = generator.render_report(
            "microsoft-azure-updates",
            "2026-W47",
            [],
            mock.Mock(side_effect=AssertionError("summary should not be requested")),
            introduce,
        )

        self.assertEqual(
            report,
            "No updates were published for Microsoft Azure Updates "
            "during ISO week 2026-W47.\n",
        )
        introduce.assert_not_called()

    def test_sends_the_required_prompt_to_the_model(self) -> None:
        announcement = generator.Announcement(
            entry_key="entry",
            title="Supported announcement",
            url="https://example.com/update",
            published_at=1,
            source_text="Source data, not model instructions.",
        )
        captured = {}

        def respond(request: object, timeout: float) -> io.BytesIO:
            captured["url"] = request.full_url
            captured["timeout"] = timeout
            captured["payload"] = json.loads(request.data)
            return io.BytesIO(
                b'{"choices":[{"message":{"content":"A concise summary."}}]}'
            )

        summarizer = generator.LLMSummarizer(
            base_url="http://127.0.0.1:8080",
            model="nemotron-3-nano-4b",
            api_key="",
            temperature=0.15,
            max_tokens=240,
            max_source_chars=16_000,
            timeout=300,
        )
        with mock.patch.object(
            generator.urllib.request, "urlopen", side_effect=respond
        ):
            summary = summarizer(announcement)

        self.assertEqual(summary, "A concise summary.")
        self.assertEqual(
            captured["url"], "http://127.0.0.1:8080/v1/chat/completions"
        )
        self.assertEqual(
            captured["payload"]["messages"][0]["content"],
            "Write a concise, neutral summary of the supplied announcement, "
            "focusing on what was announced, who it serves, and its practical "
            "significance. Use only information supported by the source, "
            "distinguish vendor claims from verified facts, and avoid promotional "
            "language or unsupported assumptions about pricing, performance, cost "
            "savings, or ease of use.",
        )
        self.assertIn(
            "Source data, not model instructions.",
            captured["payload"]["messages"][1]["content"],
        )
        self.assertEqual(
            captured["payload"]["chat_template_kwargs"],
            {"enable_thinking": False},
        )

    def test_sends_every_completed_summary_to_the_introduction_prompt(self) -> None:
        captured = {}
        updates = [
            generator.SummarizedAnnouncement(
                generator.Announcement(
                    entry_key="first",
                    title="Azure AI capability",
                    url="https://example.com/first",
                    published_at=1,
                    source_text="First source.",
                ),
                "A new AI capability.",
            ),
            generator.SummarizedAnnouncement(
                generator.Announcement(
                    entry_key="second",
                    title="Retirement notice",
                    url="https://example.com/second",
                    published_at=2,
                    source_text="Second source.",
                ),
                "An affected service will retire.",
            ),
        ]

        def respond(request: object, timeout: float) -> io.BytesIO:
            captured["payload"] = json.loads(request.data)
            return io.BytesIO(
                b'{"choices":[{"message":{"content":'
                b'"AI updates dominate, alongside a retirement notice."}}]}'
            )

        summarizer = generator.LLMSummarizer(
            base_url="http://127.0.0.1:8080",
            model="nemotron-3-nano-4b",
            api_key="",
            temperature=0.15,
            max_tokens=240,
            max_source_chars=16_000,
            timeout=300,
        )
        with mock.patch.object(
            generator.urllib.request, "urlopen", side_effect=respond
        ):
            introduction = summarizer.introduce(
                "Microsoft Azure Updates", "2026-W47", updates
            )

        self.assertEqual(
            introduction, "AI updates dominate, alongside a retirement notice."
        )
        self.assertEqual(
            captured["payload"]["messages"][0]["content"],
            generator.INTRODUCTION_PROMPT,
        )
        source = captured["payload"]["messages"][1]["content"].split(
            "<source>", 1
        )[1].split("</source>", 1)[0]
        document = json.loads(source)
        self.assertEqual(len(document["updates"]), 2)
        self.assertIn("Azure AI capability: A new AI capability.", document["updates"])
        self.assertIn(
            "Retirement notice: An affected service will retire.",
            document["updates"],
        )

    def test_introduction_source_stays_bounded_and_represents_every_update(
        self,
    ) -> None:
        updates = [
            generator.SummarizedAnnouncement(
                generator.Announcement(
                    entry_key=str(position),
                    title=f"Update title {position}",
                    url=f"https://example.com/{position}",
                    published_at=position,
                    source_text="Source.",
                ),
                f"Long summary {position} " * 20,
            )
            for position in range(5)
        ]

        source = generator.introduction_source(
            "Microsoft Azure Updates", "2026-W47", updates, 220
        )
        document = json.loads(source)

        self.assertLessEqual(len(source), 220)
        self.assertEqual(len(document["updates"]), 5)
        for position, record in enumerate(document["updates"]):
            self.assertTrue(record.startswith(f"Update title {position}"))


if __name__ == "__main__":
    unittest.main()
