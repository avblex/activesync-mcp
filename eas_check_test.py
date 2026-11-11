import io
import json
import unittest

import eas_check


def messages(*items):
    return io.StringIO("".join(json.dumps(item) + "\n" for item in items))


class MCPConnectionTest(unittest.TestCase):
    def test_request_writes_ndjson_and_returns_matching_response(self):
        output = io.StringIO()
        connection = eas_check.MCPConnection(
            messages(
                {"jsonrpc": "2.0", "method": "notifications/progress", "params": {}},
                {"jsonrpc": "2.0", "id": 1, "result": {"tools": []}},
            ),
            output,
        )

        result = connection.request("tools/list", {})

        self.assertEqual(result, {"tools": []})
        self.assertEqual(
            json.loads(output.getvalue()),
            {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}},
        )

    def test_request_surfaces_json_rpc_error(self):
        connection = eas_check.MCPConnection(
            messages({"jsonrpc": "2.0", "id": 1, "error": {"code": -32602, "message": "bad account"}}),
            io.StringIO(),
        )

        with self.assertRaisesRegex(eas_check.MCPError, r"-32602.*bad account"):
            connection.request("tools/call", {})

    def test_request_rejects_end_of_stream(self):
        connection = eas_check.MCPConnection(io.StringIO(""), io.StringIO())

        with self.assertRaisesRegex(eas_check.MCPError, "closed stdout"):
            connection.request("initialize", {})


class ResultTest(unittest.TestCase):
    def test_tool_error_is_reported_as_failure(self):
        result = {
            "isError": True,
            "content": [{"type": "text", "text": 'manager: account "work": provision: HTTP 403'}],
        }

        with self.assertRaisesRegex(eas_check.MCPError, "provision: HTTP 403"):
            eas_check.ensure_tool_success(result)

    def test_tool_payload_decodes_text_content(self):
        result = {"content": [{"type": "text", "text": '{"folders":[{"id":"5"}]}' }]}

        self.assertEqual(eas_check.tool_payload(result), {"folders": [{"id": "5"}]})


if __name__ == "__main__":
    unittest.main()
