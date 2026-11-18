//! Persona rendering.
//!
//! An agent's system prompt is written once and used in many projects, so it can
//! name the things that change per session. Rendering happens here, on the way
//! to the runtime, instead of at save time: the saved text stays what the user
//! typed, and every turn resolves against the session it actually runs in.

/// Values a persona prompt may name.
#[derive(Debug, Clone)]
pub struct PersonaVars<'a> {
    /// Directory the session runs in.
    pub repo_path: &'a str,
    /// Model id this turn uses.
    pub model: &'a str,
    /// Agent's display name; empty when the session runs without one.
    pub agent: &'a str,
    /// `YYYY-MM-DD`, for prompts that ask the model to date its conclusions.
    pub date: &'a str,
}

impl<'a> PersonaVars<'a> {
    /// The variables this build understands, without the braces.
    pub const NAMES: [&'static str; 4] = ["repo_path", "model", "agent", "date"];

    fn value(&self, name: &str) -> Option<&str> {
        match name {
            "repo_path" => Some(self.repo_path),
            "model" => Some(self.model),
            "agent" => Some(self.agent),
            "date" => Some(self.date),
            _ => None,
        }
    }
}

/// Replace `{{name}}` for every variable this build knows.
///
/// An unknown placeholder is left exactly as written. Dropping it would quietly
/// change the user's prompt, and a prompt that says `{{target_version}}` must
/// read that way until a value exists for it.
#[must_use]
pub fn render(prompt: &str, vars: &PersonaVars<'_>) -> String {
    if !prompt.contains("{{") {
        return prompt.to_string();
    }
    let mut out = String::with_capacity(prompt.len());
    let mut rest = prompt;
    while let Some(start) = rest.find("{{") {
        let after = &rest[start + 2..];
        let Some(end) = after.find("}}") else {
            break;
        };
        let name = after[..end].trim();
        out.push_str(&rest[..start]);
        match vars.value(name) {
            Some(value) => out.push_str(value),
            // Keep the placeholder, braces included.
            None => {
                out.push_str("{{");
                out.push_str(&after[..end]);
                out.push_str("}}");
            }
        }
        rest = &after[end + 2..];
    }
    out.push_str(rest);
    out
}

/// The worktree a persona should name, falling back to the session directory.
#[must_use]
pub fn display_path(cwd: &str) -> &str {
    let trimmed = cwd.trim();
    if trimmed.is_empty() { "." } else { trimmed }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn vars<'a>() -> PersonaVars<'a> {
        PersonaVars {
            repo_path: "D:\\work\\api",
            model: "auto/gpt-5.6-sol",
            agent: "迁移工程师",
            date: "2026-11-15",
        }
    }

    #[test]
    fn known_placeholders_are_replaced() {
        let text = render(
            "仓库 {{repo_path}}，模型 {{model}}，代理 {{agent}}，日期 {{date}}",
            &vars(),
        );
        assert_eq!(
            text,
            "仓库 D:\\work\\api，模型 auto/gpt-5.6-sol，代理 迁移工程师，日期 2026-11-15"
        );
    }

    #[test]
    fn unknown_placeholders_survive_verbatim() {
        let text = render("目标版本 {{target_version}}，仓库 {{repo_path}}", &vars());
        assert_eq!(text, "目标版本 {{target_version}}，仓库 D:\\work\\api");
    }

    #[test]
    fn text_without_placeholders_is_untouched() {
        let prompt = "你是一名代码迁移工程师。\n先定位调用面。";
        assert_eq!(render(prompt, &vars()), prompt);
    }

    #[test]
    fn an_unterminated_placeholder_does_not_eat_the_prompt() {
        let text = render("前缀 {{repo_path", &vars());
        assert_eq!(text, "前缀 {{repo_path");
    }

    #[test]
    fn an_empty_value_renders_as_empty() {
        let mut v = vars();
        v.agent = "";
        assert_eq!(render("[{{agent}}]", &v), "[]");
    }

    #[test]
    fn a_blank_directory_is_the_current_one() {
        assert_eq!(display_path("  "), ".");
        assert_eq!(display_path("D:\\x"), "D:\\x");
    }
}
