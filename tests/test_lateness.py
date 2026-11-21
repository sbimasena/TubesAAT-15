import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


MODULE_PATH = Path(__file__).resolve().parents[1] / "scripts/m2/lateness.py"
SPEC = importlib.util.spec_from_file_location("lateness", MODULE_PATH)
lateness = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(lateness)


def frame(kind, payload, offset=None):
    offset_line = f"id: {offset}\n" if offset is not None else ""
    return (
        f"event: {kind}\n"
        f"{offset_line}"
        f"data: {json.dumps(payload, separators=(',', ':'))}\n\n"
    )


def detection(day, clock, latitude):
    return {
        "satellite": "N20",
        "acq_date": day,
        "acq_time": clock,
        "latitude": latitude,
        "longitude": 137.96748,
        "confidence": "n",
    }


class LatenessTest(unittest.TestCase):
    def analyse_text(self, text):
        with tempfile.TemporaryDirectory() as directory:
            stream = Path(directory) / "stream.sse"
            stream.write_text(text, encoding="utf-8")
            return lateness.analyse(stream)

    def test_only_unique_acquisitions_advance_watermark(self):
        first = detection("2026-11-12", 0, -4.1)
        second = detection("2026-11-12", 10, -4.2)
        late = detection("2026-11-10", 0, -4.3)
        text = "".join(
            [
                frame("detection", {"detection": first}),
                frame("detection", {"detection": second}),
                frame("detection", {"detection": detection("2026-11-12", 5, -4.4)}),
                frame(
                    "update",
                    {"key": "N20|2026-11-12|10|-4.2|137.96748",
                     "changes": {"frp": 12.5}},
                ),
                frame("retract", {"key": "N20|2026-11-12|0|-4.1|137.96748"}),
                frame("detection", {"detection": second}),
                frame("detection", {"detection": late}),
            ]
        )

        result = self.analyse_text(text)

        self.assertEqual(result["eligible_detections"], 4)
        self.assertEqual(result["out_of_order_detections"], 2)
        self.assertEqual(result["duplicate_detections_excluded"], 1)
        self.assertEqual(result["events"]["update"], 1)
        self.assertEqual(result["events"]["retract"], 1)
        self.assertEqual(result["detections_beyond_lateness_threshold"], 1)
        self.assertEqual(result["lag_seconds"]["p50"], 0)
        self.assertEqual(result["lag_seconds"]["p99"], 173400)
        self.assertEqual(
            result["measurement"],
            "event_time_disorder_not_acquisition_to_ingest_latency",
        )

    def test_exact_lateness_boundary_is_not_counted_as_late(self):
        text = "".join(
            [
                frame("detection", {"detection": detection("2026-11-12", 2359, -4.1)}),
                frame("detection", {"detection": detection("2026-11-11", 2359, -4.2)}),
            ]
        )

        result = self.analyse_text(text)

        self.assertEqual(result["lag_seconds"]["max"], 86_400)
        self.assertEqual(result["detections_beyond_lateness_threshold"], 0)

    def test_replayed_offsets_are_excluded_and_gaps_are_reported(self):
        first = {"detection": detection("2026-11-12", 0, -4.1)}
        second = {"detection": detection("2026-11-12", 10, -4.2)}
        text = (
            frame("detection", first, 7)
            + frame("detection", first, 7)
            + frame("detection", second, 9)
        )

        result = self.analyse_text(text)

        self.assertEqual(result["events"]["detection"], 2)
        self.assertEqual(result["eligible_detections"], 2)
        self.assertEqual(result["duplicate_offsets_excluded"], 1)
        self.assertEqual(result["offset_gaps"], 1)
        self.assertEqual((result["first_offset"], result["last_offset"]), (7, 9))

    def test_multiline_sse_data_and_unterminated_final_event(self):
        payload_lines = (
            '{"offset":5,',
            '"detection":{"satellite":"N20","acq_date":"2026-11-12",'
            '"acq_time":243,"latitude":-4.100,"longitude":137.96748,'
            '"confidence":"n"}}',
        )
        raw_event = "event: detection\n" + "".join(
            f"data: {line}\n" for line in payload_lines
        )
        result = self.analyse_text(raw_event)

        self.assertEqual(result["eligible_detections"], 1)
        self.assertEqual(result["lag_seconds"]["p99"], 0)
        self.assertEqual(result["unparsed_detections"], 0)

    def test_malformed_detection_is_reported_without_stopping_next_event(self):
        text = (
            "event: detection\ndata: {broken}\n\n"
            + frame("detection", {"detection": detection("2026-11-12", 243, -4.1)})
        )

        result = self.analyse_text(text)

        self.assertEqual(result["unparsed_detections"], 1)
        self.assertEqual(result["eligible_detections"], 1)

    def test_empty_sample_has_no_percentiles_or_candidate(self):
        result = self.analyse_text("")

        self.assertEqual(result["eligible_detections"], 0)
        self.assertIsNone(result["lag_seconds"]["p99"])


if __name__ == "__main__":
    unittest.main()
