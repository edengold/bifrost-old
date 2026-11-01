/**
 * CEL → RuleGroup reverse parser for routing rules.
 *
 * The visual builder stores no query state when a rule was authored (or edited)
 * in raw CEL mode, so switching to the builder used to discard the expression
 * entirely. This parser reconstructs an editable rule group from the CEL subset
 * the forward converter (celConverterRouting.convertRuleToCEL) can emit:
 *
 *   field == "v" | field != "v" | field > n | field.startsWith("p") |
 *   field.contains("s") | field.matches("re") | field in ["a","b"] |
 *   headers["h"] == "v" | params["p"] matches "re" |
 *   ("P" in provider_health && provider_health["P"]["*"].healthy)   [optional !]
 *
 * Anything it cannot map into the builder's field set returns null and the
 * caller keeps the hand-written CEL instead of destroying it.
 */

import { RuleGroupType, RuleType } from "react-querybuilder";

interface TopSplit {
	clauses: string[];
	combinator: "and" | "or";
}

/**
 * Split a CEL expression into top-level clauses joined by && / ||, respecting
 * balanced parentheses and quoted strings. Returns null when parentheses are
 * unbalanced or a single level mixes && and || (the builder has one combinator
 * per group; mixed precedence without explicit parens is not representable).
 */
function splitTopLevel(src: string): TopSplit | null {
	const clauses: string[] = [];
	const seen: Array<"and" | "or"> = [];
	let depth = 0;
	let inQuote: '"' | "'" | null = null;
	let current = "";
	for (let i = 0; i < src.length; i++) {
		const ch = src[i];
		if (inQuote) {
			if (ch === inQuote) inQuote = null;
			current += ch;
			continue;
		}
		if (ch === '"' || ch === "'") {
			inQuote = ch;
			current += ch;
			continue;
		}
		if (ch === "(") depth++;
		if (ch === ")") {
			depth--;
			if (depth < 0) return null;
		}
		if (depth === 0 && ch === "&" && src[i + 1] === "&") {
			clauses.push(current.trim());
			seen.push("and");
			current = "";
			i++;
			continue;
		}
		if (depth === 0 && ch === "|" && src[i + 1] === "|") {
			clauses.push(current.trim());
			seen.push("or");
			current = "";
			i++;
			continue;
		}
		current += ch;
	}
	if (depth !== 0 || inQuote) return null;
	if (current.trim().length > 0) clauses.push(current.trim());
	if (clauses.length === 0) return null;
	const unique = new Set(seen);
	if (unique.size > 1) return null;
	return { clauses, combinator: seen.length > 0 ? seen[0] : "and" };
}

/**
 * Normalize provider-health suffix forms so only the canonical pair remains:
 *   ...healthy == false  →  !(...)
 *   ...healthy == true   →  (...)
 */
function normalizeProviderHealth(src: string): string {
	return src
		.replace(
			/"([^"]+)"\s+in\s+provider_health\s*&&\s*provider_health\["\1"\]\["\*"\]\.healthy\s*==\s*false/g,
			`!("$1" in provider_health && provider_health["$1"]["*"].healthy)`,
		)
		.replace(
			/"([^"]+)"\s+in\s+provider_health\s*&&\s*provider_health\["\1"\]\["\*"\]\.healthy\s*==\s*true/g,
			`("$1" in provider_health && provider_health["$1"]["*"].healthy)`,
		);
}

/**
 * Strip an outer negation plus redundant wrapping parentheses.
 */
function unwrap(src: string): { expr: string; negated: boolean } {
	let expr = src.trim();
	let negated = false;
	for (;;) {
		if (expr.startsWith("!(") && expr.endsWith(")")) {
			const inner = splitTopLevel(expr.slice(2, -1));
			if (inner) {
				expr = expr.slice(2, -1).trim();
				negated = !negated;
				continue;
			}
		}
		if (expr.startsWith("(") && expr.endsWith(")")) {
			const inner = splitTopLevel(expr.slice(1, -1));
			if (inner) {
				expr = expr.slice(1, -1).trim();
				continue;
			}
		}
		break;
	}
	return { expr, negated };
}

/**
 * Map one clause to a builder rule. Returns null when the clause is not
 * representable.
 */
function clauseToRule(cond: string): RuleType | null {
	const s = cond.trim();

	// provider_healthy canonical pair: "P" in provider_health && provider_health["P"]["*"].healthy
	let m = s.match(
		/^"([^"]+)"\s+in\s+provider_health\s*&&\s*provider_health\["([^"]+)"\]\["\*"\]\.healthy$/,
	);
	if (m && m[1] === m[2]) {
		return { field: "provider_healthy", operator: "=", value: m[1], valueSource: "value" } as unknown as RuleType;
	}

	// headers["h"] == "v" / params["p"] == "v"
	m = s.match(/^(headers|params)\[["']([^"']+)["']\]\s*(==|!=)\s*"([^"]*)"$/);
	if (m) {
		return {
			field: m[1],
			operator: m[3] === "==" ? "=" : "!=",
			value: `${m[2]}:${m[4]}`,
			valueSource: "value",
		} as unknown as RuleType;
	}

	// headers["h"].startsWith("v") etc.
	m = s.match(/^(headers|params)\[["']([^"']+)["']\]\.(startsWith|endsWith|contains|matches)\("([^"]*)"\)$/);
	if (m) {
		const opName = m[3] === "startsWith" ? "beginsWith" : m[3];
		return {
			field: m[1],
			operator: opName,
			value: `${m[2]}:${m[3] === "matches" ? m[4] : m[4]}`,
			valueSource: "value",
		} as unknown as RuleType;
	}

	// plain scalar equality: field == "v" / field != "v"
	m = s.match(/^([a-z_]+)\s*(==|!=)\s*"([^"]*)"$/);
	if (m) {
		return { field: m[1], operator: m[2] === "==" ? "=" : "!=", value: m[3], valueSource: "value" } as unknown as RuleType;
	}

	// numeric comparisons (double literals normalize: 50.0 → 50)
	m = s.match(/^([a-z_]+)\s*(>=|<=|>|<)\s*(\d+(?:\.\d+)?)$/);
	if (m) {
		const num = parseFloat(m[3]);
		return { field: m[1], operator: m[2], value: num.toString(), valueSource: "value" } as unknown as RuleType;
	}

	// string methods: field.method("arg")
	m = s.match(/^([a-z_]+)\.(startsWith|endsWith|contains|matches)\("([^"]*)"\)$/);
	if (m) {
		const opName = m[2] === "startsWith" ? "beginsWith" : m[2];
		return { field: m[1], operator: opName, value: m[3], valueSource: "value" } as unknown as RuleType;
	}

	// field in ["a", "b"]
	m = s.match(/^([a-z_]+)\s+in\s+\[(.*)\]$/);
	if (m) {
		const items = m[2]
			.split(",")
			.map((x) => x.trim().replace(/^["']|["']$/g, ""))
			.filter((x) => x);
		if (items.length > 0) {
			return { field: m[1], operator: "in", value: items, valueSource: "value" } as unknown as RuleType;
		}
	}

	return null;
}

function parseExpr(src: string): RuleGroupType | null {
	const { expr, negated } = unwrap(src);

	// Only the provider-health pair is invertible as a single builder row;
	// any other negation is not representable.
	const providerHealth = expr.match(
		/^"([^"]+)"\s+in\s+provider_health\s*&&\s*provider_health\["([^"]+)"\]\["\*"\]\.healthy$/,
	);
	if (providerHealth && providerHealth[1] === providerHealth[2]) {
		return {
			combinator: "and",
			rules: [
				{
					field: "provider_healthy",
					operator: negated ? "!=" : "=",
					value: providerHealth[1],
					valueSource: "value",
				} as unknown as RuleType,
			],
		};
	}
	if (negated) return null;

	const split = splitTopLevel(expr);
	if (!split) return null;
	const { clauses, combinator } = split;

	const rules: Array<RuleType | RuleGroupType> = [];
	for (const clause of clauses) {
		const rule = clauseToRule(clause);
		if (rule) {
			rules.push(rule);
			continue;
		}
		// Try a nested parenthesized group — but only when this clause is a
		// strict sub-expression (guard against trivial recursion).
		if (clause !== expr && clause.startsWith("(") && clause.endsWith(")")) {
			const nested = parseExpr(clause);
			if (nested) {
				rules.push(nested);
				continue;
			}
		}
		return null;
	}
	if (rules.length === 0) return null;
	return { combinator, rules };
}

/**
 * Parse a CEL expression into a builder query. Returns null when the expression
 * uses anything the builder cannot represent — callers must treat null as
 * "keep the raw CEL" rather than discarding it.
 */
export function celToRuleGroup(cel: string): RuleGroupType | null {
	if (!cel || !cel.trim()) return null;
	try {
		return parseExpr(normalizeProviderHealth(cel.trim()));
	} catch {
		return null;
	}
}
