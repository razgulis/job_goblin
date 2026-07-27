from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import analyze_jobs


def make_config(
    *,
    api_mode: str = "responses",
    reasoning_effort: str = "medium",
    model: str = "gpt-5.6-terra",
    base_url: str = "https://api.openai.com/v1",
) -> analyze_jobs.Config:
    return analyze_jobs.Config(
        resume_file="candidate.md",
        resume_path=Path("/tmp/candidate.md"),
        job_urls=["https://jobs.example.com/search"],
        llm_base_url=base_url,
        llm_api_key="test-key",
        llm_model=model,
        llm_api_mode=api_mode,
        llm_reasoning_effort=reasoning_effort,
        scrape_timeout_seconds=20,
        llm_timeout_seconds=60,
        max_jobs_per_source=100,
        workday_page_size=20,
        max_new_evaluations_per_run=40,
    )


def evaluation_json() -> str:
    return json.dumps(
        {
            "job_title": "Platform Engineer",
            "company": "Example",
            "fit_score": 82,
            "should_apply": True,
            "summary": "Strong fit.",
            "matched_skills": ["Python"],
            "missing_skills": ["Go"],
            "experience_alignment": "Relevant platform experience.",
            "concerns": [],
            "recommended_resume_tweaks": ["Highlight infrastructure work."],
        }
    )


class CaptureCreate:
    def __init__(self, response: object) -> None:
        self.response = response
        self.requests: list[dict[str, object]] = []

    def create(self, *args: object, **kwargs: object) -> object:
        if args:
            kwargs["_args"] = args
        self.requests.append(kwargs)
        return self.response


class CaptureEvents:
    def __init__(self) -> None:
        self.events: list[tuple[str, str, dict[str, object]]] = []

    def log(
        self,
        event_type: str,
        message: str,
        **fields: object,
    ) -> None:
        self.events.append((event_type, message, fields))


class WorkdayURLTests(unittest.TestCase):
    def test_jobs_url_with_facets_and_no_search_text_is_a_collection(self) -> None:
        url = (
            "https://workday.wd5.myworkdayjobs.com/en-US/Workday/jobs"
            "?redirect=/en-US/Workday/userHome"
            "&locations=4d3a30f878c5011d15d8cafbd5810000"
            "&locations=62a48cfecb41101e2011999af07c4fdb"
        )

        source = analyze_jobs.parse_workday_search_source(url)

        self.assertIsNotNone(source)
        assert source is not None
        self.assertEqual(source.tenant, "workday")
        self.assertEqual(source.site, "Workday")
        self.assertEqual(source.search_text, "")
        self.assertEqual(
            source.applied_facets,
            {
                "locations": [
                    "4d3a30f878c5011d15d8cafbd5810000",
                    "62a48cfecb41101e2011999af07c4fdb",
                ]
            },
        )

    def test_crowdstrike_jobs_url_preserves_search_and_location(self) -> None:
        url = (
            "https://crowdstrike.wd5.myworkdayjobs.com/en-US/"
            "crowdstrikecareers/jobs?q=-analyst+-sales+-manager"
            "&locations=20feac86ebdd0102586dc95b42138d6f"
        )

        source = analyze_jobs.parse_workday_search_source(url)

        self.assertIsNotNone(source)
        assert source is not None
        self.assertEqual(source.tenant, "crowdstrike")
        self.assertEqual(source.site, "crowdstrikecareers")
        self.assertEqual(source.search_text, "-analyst -sales -manager")
        self.assertEqual(
            source.applied_facets,
            {"locations": ["20feac86ebdd0102586dc95b42138d6f"]},
        )

    def test_nvidia_jobs_url_is_a_collection(self) -> None:
        url = (
            "https://nvidia.wd5.myworkdayjobs.com/en-US/"
            "nvidiaexternalcareersite/jobs?q=Engineer"
            "&locations=d2088e737cbb01d5e2be9e52ce01926f"
            "&locations=91336993fab910af6d712ddeebf4c38e"
        )

        source = analyze_jobs.parse_workday_search_source(url)

        self.assertIsNotNone(source)
        assert source is not None
        self.assertEqual(source.tenant, "nvidia")
        self.assertEqual(source.site, "nvidiaexternalcareersite")
        self.assertEqual(source.search_text, "Engineer")
        self.assertEqual(len(source.applied_facets["locations"]), 2)

    def test_direct_workday_job_url_is_not_treated_as_a_collection(self) -> None:
        url = (
            "https://example.wd5.myworkdayjobs.com/en-US/site/"
            "job/Atlanta/Platform-Engineer_R123"
        )

        self.assertIsNone(analyze_jobs.parse_workday_search_source(url))

    def test_collection_payload_supports_empty_search_text(self) -> None:
        source = analyze_jobs.WorkdaySearchSource(
            original_url="https://example.wd5.myworkdayjobs.com/en-US/site/jobs",
            base_url="https://example.wd5.myworkdayjobs.com",
            tenant="example",
            site="site",
            locale="en-US",
            search_text="",
            applied_facets={"locations": ["location-id"]},
        )
        response = SimpleNamespace(
            raise_for_status=lambda: None,
            json=lambda: {"total": 0, "jobPostings": []},
        )
        post = CaptureCreate(response)
        session = SimpleNamespace(post=post.create)

        postings, total = analyze_jobs.list_workday_postings(
            session,
            source,
            make_config(),
        )

        self.assertEqual(postings, [])
        self.assertEqual(total, 0)
        request = post.requests[0]
        self.assertEqual(request["json"]["searchText"], "")  # type: ignore[index]
        self.assertEqual(
            request["json"]["appliedFacets"],  # type: ignore[index]
            {"locations": ["location-id"]},
        )

    def test_legacy_search_page_placeholder_is_removed(self) -> None:
        url = "https://example.wd5.myworkdayjobs.com/en-US/site/jobs?q=Engineer"
        job_id = f"url:{analyze_jobs.stable_hash(url)}"
        state = {
            job_id: {
                "job_id": job_id,
                "source_url": url,
                "job_url": url,
                "job_req_id": "",
                "title": "Careers at Example",
            }
        }
        events = CaptureEvents()

        removed = analyze_jobs.remove_legacy_source_placeholder(
            state,
            url,
            False,
            events,
        )

        self.assertTrue(removed)
        self.assertNotIn(job_id, state)
        self.assertEqual(events.events[0][0], "legacy_source_placeholder_removed")


class AnalyzerLLMTests(unittest.TestCase):
    def test_openai_configuration_defaults_to_responses_and_medium(self) -> None:
        config = self.load_temporary_config("https://api.openai.com/v1")

        self.assertEqual(config.llm_api_mode, "responses")
        self.assertEqual(config.llm_reasoning_effort, "medium")

    def test_custom_provider_defaults_to_chat_and_omitted_reasoning(self) -> None:
        config = self.load_temporary_config("https://llm.example.com/v1")

        self.assertEqual(config.llm_api_mode, "chat_completions")
        self.assertEqual(config.llm_reasoning_effort, "default")

    def load_temporary_config(self, base_url: str) -> analyze_jobs.Config:
        temporary_directory = tempfile.TemporaryDirectory()
        self.addCleanup(temporary_directory.cleanup)
        root = Path(temporary_directory.name)
        resume_dir = root / "resume"
        resume_dir.mkdir()
        (resume_dir / "candidate.md").write_text("Resume", encoding="utf-8")
        env_path = root / ".env"
        env_path.write_text(
            "\n".join(
                [
                    "RESUME_FILE=candidate.md",
                    "JOB_URLS=https://jobs.example.com/search",
                    f"LLM_BASE_URL={base_url}",
                ]
            ),
            encoding="utf-8",
        )

        with (
            mock.patch.object(analyze_jobs, "ENV_PATH", env_path),
            mock.patch.object(analyze_jobs, "RESUME_DIR", resume_dir),
            mock.patch.dict(os.environ, {}, clear=True),
        ):
            return analyze_jobs.load_config(require_llm=False)

    def test_responses_request_uses_reasoning_schema_and_no_storage(self) -> None:
        create = CaptureCreate(
            SimpleNamespace(
                status="completed",
                output_text=evaluation_json(),
                output=[],
            )
        )
        client = SimpleNamespace(responses=SimpleNamespace(create=create.create))
        messages = [
            {"role": "system", "content": "System instructions"},
            {"role": "user", "content": "Evaluate this job"},
        ]

        content = analyze_jobs.create_llm_response(
            client,
            make_config(),
            messages,
        )

        self.assertEqual(content, evaluation_json())
        request = create.requests[0]
        self.assertEqual(request["model"], "gpt-5.6-terra")
        self.assertEqual(request["instructions"], "System instructions")
        self.assertEqual(request["input"], "Evaluate this job")
        self.assertEqual(request["reasoning"], {"effort": "medium"})
        self.assertIs(request["store"], False)
        self.assertNotIn("temperature", request)
        response_format = request["text"]["format"]  # type: ignore[index]
        self.assertEqual(response_format["type"], "json_schema")
        self.assertIs(response_format["strict"], True)
        self.assertFalse(response_format["schema"]["additionalProperties"])

    def test_default_reasoning_effort_is_omitted(self) -> None:
        create = CaptureCreate(
            SimpleNamespace(
                status="completed",
                output_text=evaluation_json(),
                output=[],
            )
        )
        client = SimpleNamespace(responses=SimpleNamespace(create=create.create))

        analyze_jobs.create_llm_response(
            client,
            make_config(reasoning_effort="default"),
            [
                {"role": "system", "content": "System"},
                {"role": "user", "content": "User"},
            ],
        )

        self.assertNotIn("reasoning", create.requests[0])

    def test_chat_completions_uses_reasoning_effort_without_temperature(self) -> None:
        create = CaptureCreate(
            SimpleNamespace(
                choices=[
                    SimpleNamespace(
                        message=SimpleNamespace(content=evaluation_json())
                    )
                ]
            )
        )
        client = SimpleNamespace(
            chat=SimpleNamespace(
                completions=SimpleNamespace(create=create.create)
            )
        )

        content = analyze_jobs.create_llm_response(
            client,
            make_config(api_mode="chat_completions", reasoning_effort="low"),
            [
                {"role": "system", "content": "System"},
                {"role": "user", "content": "User"},
            ],
        )

        self.assertEqual(content, evaluation_json())
        request = create.requests[0]
        self.assertEqual(request["reasoning_effort"], "low")
        self.assertNotIn("temperature", request)
        self.assertEqual(request["response_format"], {"type": "json_object"})

    def test_chat_default_reasoning_effort_is_omitted(self) -> None:
        create = CaptureCreate(
            SimpleNamespace(
                choices=[
                    SimpleNamespace(
                        message=SimpleNamespace(content=evaluation_json())
                    )
                ]
            )
        )
        client = SimpleNamespace(
            chat=SimpleNamespace(
                completions=SimpleNamespace(create=create.create)
            )
        )

        analyze_jobs.create_llm_response(
            client,
            make_config(
                api_mode="chat_completions",
                reasoning_effort="default",
            ),
            [
                {"role": "system", "content": "System"},
                {"role": "user", "content": "User"},
            ],
        )

        self.assertNotIn("reasoning_effort", create.requests[0])

    def test_incomplete_response_reports_the_reason(self) -> None:
        create = CaptureCreate(
            SimpleNamespace(
                status="incomplete",
                incomplete_details=SimpleNamespace(reason="max_output_tokens"),
                output_text="",
                output=[],
            )
        )
        client = SimpleNamespace(responses=SimpleNamespace(create=create.create))

        with self.assertRaisesRegex(RuntimeError, "max_output_tokens"):
            analyze_jobs.create_llm_response(
                client,
                make_config(),
                [
                    {"role": "system", "content": "System"},
                    {"role": "user", "content": "User"},
                ],
            )

    def test_refusal_is_reported(self) -> None:
        create = CaptureCreate(
            SimpleNamespace(
                status="completed",
                output_text="",
                output=[
                    SimpleNamespace(
                        content=[
                            SimpleNamespace(
                                refusal="Cannot evaluate this content."
                            )
                        ]
                    )
                ],
            )
        )
        client = SimpleNamespace(responses=SimpleNamespace(create=create.create))

        with self.assertRaisesRegex(RuntimeError, "Cannot evaluate"):
            analyze_jobs.create_llm_response(
                client,
                make_config(),
                [
                    {"role": "system", "content": "System"},
                    {"role": "user", "content": "User"},
                ],
            )

    def test_evaluation_fingerprint_covers_all_semantic_inputs(self) -> None:
        base = analyze_jobs.build_evaluation_fingerprint(
            make_config(),
            "resume-a",
            "job-a",
        )
        variants = [
            analyze_jobs.build_evaluation_fingerprint(
                make_config(reasoning_effort="low"),
                "resume-a",
                "job-a",
            ),
            analyze_jobs.build_evaluation_fingerprint(
                make_config(model="gpt-5.6-sol"),
                "resume-a",
                "job-a",
            ),
            analyze_jobs.build_evaluation_fingerprint(
                make_config(base_url="https://llm.example.com/v1"),
                "resume-a",
                "job-a",
            ),
            analyze_jobs.build_evaluation_fingerprint(
                make_config(),
                "resume-b",
                "job-a",
            ),
            analyze_jobs.build_evaluation_fingerprint(
                make_config(),
                "resume-a",
                "job-b",
            ),
        ]

        self.assertNotIn(base, variants)
        self.assertEqual(len(set(variants)), len(variants))

    def test_changed_fingerprint_clears_cached_state(self) -> None:
        prior = {
            "job_id": "job-1",
            "content_hash": "same-content",
            "evaluation_fingerprint": "old-fingerprint",
            "fit_score": "82",
            "should_apply": "true",
            "last_evaluated_at": "2026-07-26T10:00:00-06:00",
            "analysis_path": "analyses/job-1.json",
            "model": "gpt-5.4",
        }
        scraped = analyze_jobs.ScrapedJob(
            url="https://jobs.example.com/job-1",
            title="Platform Engineer",
            text="Job description",
        )

        record = analyze_jobs.build_state_record(
            prior,
            scraped,
            "job-1",
            "same-content",
            "new-fingerprint",
            "2026-07-27T10:00:00-06:00",
        )

        for field in (
            "fit_score",
            "should_apply",
            "last_evaluated_at",
            "analysis_path",
            "model",
            "api_mode",
            "reasoning_effort",
            "evaluation_fingerprint",
        ):
            self.assertEqual(record[field], "", field)

    def test_cached_analysis_requires_matching_fingerprint(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            state_dir = Path(directory)
            analysis_dir = state_dir / "analyses"
            analysis_dir.mkdir()
            path = analysis_dir / "job-1.json"
            path.write_text(
                json.dumps(
                    {
                        "content_hash": "content",
                        "evaluation_fingerprint": "fingerprint-a",
                        "analysis": {"fit_score": 82},
                    }
                ),
                encoding="utf-8",
            )
            record = {
                "content_hash": "content",
                "analysis_path": "analyses/job-1.json",
            }
            original_state_dir = analyze_jobs.STATE_DIR
            analyze_jobs.STATE_DIR = state_dir
            try:
                self.assertIsNotNone(
                    analyze_jobs.load_cached_analysis(
                        record,
                        "fingerprint-a",
                    )
                )
                self.assertIsNone(
                    analyze_jobs.load_cached_analysis(
                        record,
                        "fingerprint-b",
                    )
                )
            finally:
                analyze_jobs.STATE_DIR = original_state_dir


if __name__ == "__main__":
    unittest.main()
