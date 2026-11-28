import type { Metadata } from "next";

export const siteOrigin = "https://hedvi.dev";

export function pageMetadata(path: string, title: string, description: string): Metadata {
  return { title, description, alternates: { canonical: new URL(path, siteOrigin).href } };
}

export const publicPages = {
  "/": {
    title: "Transactional email API for developers",
    description: "Send transactional email with Hedvi’s open-source API. Test messages in a private inbox, verify your sending domain and track delivery for each recipient.",
  },
  "/pricing": {
    title: "Email API pricing for Free, Flex and Pro",
    description: "Compare Hedvi’s Free monthly allowance, prepaid Flex credits and Pro subscriptions. See recipient limits, attachment allowances and prices in USD.",
  },
  "/docs": {
    title: "Transactional email API documentation and guides",
    description: "Integrate the Hedvi email API with step-by-step guides to sending, domain verification, test inboxes, webhooks, billing and migrating from Resend.",
  },
  "/legal/terms": {
    title: "Terms of Service",
    description: "Read the terms for using the hosted Hedvi email service from StackX AB, including account responsibilities, billing, service changes and termination.",
  },
  "/legal/privacy": {
    title: "Privacy Notice",
    description: "Learn how StackX AB processes personal data for Hedvi accounts, billing and operations, how long it keeps records and how to exercise your privacy rights.",
  },
  "/legal/cookies": {
    title: "Cookie Notice",
    description: "See which cookies Hedvi uses for sign-in, remembered sessions and account security, how long they last and how to manage them in your browser.",
  },
  "/legal/dpa": {
    title: "Data Processing Agreement",
    description: "Review how StackX AB processes personal data for customers using Hedvi, including processor duties, security measures and assistance with data requests.",
  },
  "/legal/subprocessors": {
    title: "Service providers and subprocessors",
    description: "Find the providers that help run Hedvi, the services they provide, where processing takes place and how customers are informed about provider changes.",
  },
  "/legal/acceptable-use": {
    title: "Acceptable Use Policy",
    description: "Review Hedvi’s rules for sending email, including recipient permission, sender identity, prohibited content and how to report suspected abuse.",
  },
} as const;

export function publicMetadata(path: keyof typeof publicPages): Metadata {
  const { title, description } = publicPages[path];
  return pageMetadata(path, title, description);
}
