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

    def test_renders_one_markdown_item_per_announcement(self) -> None:
        announcement = generator.Announcement(
            entry_key="entry",
            title="A [new] capability",
            url="https://example.com/update?id=42",
            published_at=1,
            source_text="Supported source text.",
        )
        seen: list[generator.Announcement] = []

        def summarize(value: generator.Announcement) -> str:
            seen.append(value)
            return "It provides a monitored capability."

        report = generator.render_report(
            "microsoft-azure-updates", "2026-W47", [announcement], summarize
        )

        self.assertEqual(seen, [announcement])
        self.assertIn(
            "* [A \\[new\\] capability](https://example.com/update?id=42). - "
            "It provides a monitored capability.",
            report,
        )
        self.assertTrue(report.startswith("This week featured 1 update"))

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


if __name__ == "__main__":
    unittest.main()
