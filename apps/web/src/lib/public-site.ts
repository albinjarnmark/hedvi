// Non-secret deployment values, embedded into static documentation at build time.
// Rebuild the web service after changing the public API hostname.
export const publicApiUrl = (process.env.HEDVI_PUBLIC_API_URL ?? "https://api.hedvi.dev").replace(/\/$/, "");
export const supportEmail = process.env.NEXT_PUBLIC_HEDVI_SUPPORT_EMAIL ?? "support@example.invalid";
// Fork operators set this to the corresponding source for their running build.
export const sourceUrl = process.env.NEXT_PUBLIC_HEDVI_SOURCE_URL ?? "https://github.com/albinjarnmark/hedvi";

