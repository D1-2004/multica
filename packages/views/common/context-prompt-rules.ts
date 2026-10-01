import { useT } from "../i18n";

/** Server limits of one level's prompt components (Context Builder and the
 * configure page share them). Lengths count characters (code points). */
export const PROMPT_COMPONENT_MAX = 20;
export const PROMPT_NAME_MAX_LENGTH = 64;
export const PROMPT_TEXT_MAX_LENGTH = 8000;
const PROMPT_NAME_FORBIDDEN = /\p{Cc}/u;

export type PromptProblem =
  | "name_required"
  | "name_too_long"
  | "name_invalid"
  | "name_duplicate"
  | "text_required"
  | "text_too_long"
  | "text_invalid";

/** The first rule a prompt component breaks, or null. `name` and `text` are
 * already trimmed; `otherNames` are the level's other components. */
export function promptProblem(name: string, text: string, otherNames: ReadonlySet<string>): PromptProblem | null {
  if (name === "") return "name_required";
  if ([...name].length > PROMPT_NAME_MAX_LENGTH) return "name_too_long";
  if (PROMPT_NAME_FORBIDDEN.test(name)) return "name_invalid";
  if (otherNames.has(name)) return "name_duplicate";
  if (text === "") return "text_required";
  if ([...text].length > PROMPT_TEXT_MAX_LENGTH) return "text_too_long";
  if (text.includes("\u0000")) return "text_invalid";
  return null;
}

/** Localized message of a prompt problem. */
export function usePromptProblemMessage(): (problem: PromptProblem) => string {
  const { t } = useT("agents");
  return (problem) => {
    switch (problem) {
      case "name_required":
        return t(($) => $.tab_body.context_builder.prompt_name_required);
      case "name_too_long":
        return t(($) => $.tab_body.context_builder.prompt_name_too_long, { max: PROMPT_NAME_MAX_LENGTH });
      case "name_invalid":
        return t(($) => $.tab_body.context_builder.prompt_name_invalid);
      case "name_duplicate":
        return t(($) => $.tab_body.context_builder.prompt_name_duplicate);
      case "text_required":
        return t(($) => $.tab_body.context_builder.prompt_text_required);
      case "text_too_long":
        return t(($) => $.tab_body.context_builder.prompt_text_too_long, { max: PROMPT_TEXT_MAX_LENGTH });
      case "text_invalid":
        return t(($) => $.tab_body.context_builder.prompt_text_invalid);
      default:
        return t(($) => $.tab_body.context_builder.prompt_name_invalid);
    }
  };
}
