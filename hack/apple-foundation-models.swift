import Darwin
import Foundation
import FoundationModels

struct CLIError: Error, CustomStringConvertible {
	let description: String
}

func usage() -> String {
	"""
	usage: apple-foundation-models.swift --prompt <text> [--json]
	       apple-foundation-models.swift --self-test

	Examples:
	  xcrun swift hack/apple-foundation-models.swift --prompt "Summarize this diff"
	  xcrun swift hack/apple-foundation-models.swift --self-test
	"""
}

func parseArguments() throws -> (prompt: String, json: Bool) {
	var args = Array(CommandLine.arguments.dropFirst())
	var prompt: String?
	var json = false
	var selfTest = false

	for index in args.indices {
		switch args[index] {
		case "--json":
			json = true
		case "--self-test":
			selfTest = true
		case "--prompt":
			guard index + 1 < args.count else {
				throw CLIError(description: "missing value for --prompt")
			}
			prompt = args[index + 1]
		default:
			continue
		}
	}

	if selfTest {
		return ("Reply with exactly OK", json)
	}

	if let prompt, !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
		return (prompt, json)
	}

	let stdinData = FileHandle.standardInput.readDataToEndOfFile()
	if let stdinText = String(data: stdinData, encoding: .utf8)?
		.trimmingCharacters(in: .whitespacesAndNewlines),
		!stdinText.isEmpty {
		return (stdinText, json)
	}

	throw CLIError(description: usage())
}

let parsed: (prompt: String, json: Bool)
do {
	parsed = try parseArguments()
} catch {
	fputs("\(error)\n", stderr)
	Darwin.exit(2)
}

do {
	let session = LanguageModelSession()
	let response = try await session.respond(to: parsed.prompt)
	if parsed.json {
		let payload: [String: Any] = [
			"ok": true,
			"content": response.content,
		]
		let data = try JSONSerialization.data(withJSONObject: payload, options: [.prettyPrinted, .sortedKeys])
		FileHandle.standardOutput.write(data)
		FileHandle.standardOutput.write(Data("\n".utf8))
	} else {
		print(response.content)
	}
} catch {
	fputs("apple foundation models error: \(error)\n", stderr)
	Darwin.exit(1)
}
