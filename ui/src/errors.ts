/** A short user-facing message for any thrown value. */
export function message(error: unknown): string {
  if (error && typeof error === "object") {
    const response = (error as { response?: { message?: unknown } }).response;
    if (typeof response?.message === "string" && response.message)
      return response.message;
    if (error instanceof Error && error.message) return error.message;
  }
  return "Something went wrong. Please retry.";
}
