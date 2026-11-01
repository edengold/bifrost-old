import { describe, expect, it } from "vitest";
import { celToRuleGroup } from "./celToRuleGroup";
import { convertRuleGroupToCEL } from "./celConverterRouting";

/**
 * Round-trip property: any expression the forward converter emits must parse
 * back into a group whose forward conversion is identical. The provider-health
 * pair additionally normalizes: healthy == false imports as a negated row.
 */
describe("celToRuleGroup", () => {
	it("imports the shipped devpass_down rule as one editable row", () => {
		const cel = `"LLM Gateway (DevPass)" in provider_health && provider_health["LLM Gateway (DevPass)"]["*"].healthy == false`;
		const group = celToRuleGroup(cel);
		expect(group).not.toBeNull();
		expect(group!.combinator).toBe("and");
		expect(group!.rules).toHaveLength(1);
		expect(group!.rules[0]).toMatchObject({
			field: "provider_healthy",
			operator: "!=",
			value: "LLM Gateway (DevPass)",
		});
		// The regenerated CEL must be semantically identical to the original.
		const regenerated = convertRuleGroupToCEL(group!);
		expect(regenerated).toBe(`!("LLM Gateway (DevPass)" in provider_health && provider_health["LLM Gateway (DevPass)"]["*"].healthy)`);
	});

	it("imports bare (positive) provider-health pair", () => {
		const cel = `"P" in provider_health && provider_health["P"]["*"].healthy`;
		const group = celToRuleGroup(cel);
		expect(group!.rules[0]).toMatchObject({ field: "provider_healthy", operator: "=", value: "P" });
	});

	it("round-trips every builder-convertible expression", () => {
		const cases: Array<[string, string]> = [
			[`model == "glm-5.3-flash"`, `model == "glm-5.3-flash"`],
			[`provider != "openai"`, `provider != "openai"`],
			[`budget_used > 50.0`, `budget_used > 50.0`],
			[`model.contains("sonnet")`, `model.contains("sonnet")`],
			[`model.matches("gpt-.*")`, `model.matches("gpt-.*")`],
			[`model in ["a", "b"]`, `model in ["a", "b"]`],
			[`headers["x-env"] == "prod"`, `headers["x-env"] == "prod"`],
			[`model == "m" && provider != "p"`, `model == "m" && provider != "p"`],
			[`model == "m" || provider == "p"`, `model == "m" || provider == "p"`],
		];
		for (const [cel, expected] of cases) {
			const group = celToRuleGroup(cel);
			expect(group, `parse failed: ${cel}`).not.toBeNull();
			expect(convertRuleGroupToCEL(group!), `round-trip mismatch: ${cel}`).toBe(expected);
		}
	});

	it("refuses unmappable expressions (caller keeps raw CEL)", () => {
		expect(celToRuleGroup(`provider.matches("(?i)x|y") && customFunc(model)`)).toBeNull();
		expect(celToRuleGroup(`!model.contains("x")`)).toBeNull();
		expect(celToRuleGroup(`model == "a" && provider == "b" || request_type == "embedding"`)).toBeNull();
		expect(celToRuleGroup(`key_health["k"].healthy == true`)).toBeNull();
	});

	it("returns null for empty input", () => {
		expect(celToRuleGroup("")).toBeNull();
		expect(celToRuleGroup("   ")).toBeNull();
	});
});
