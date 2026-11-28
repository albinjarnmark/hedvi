import type { MetadataRoute } from "next";
import { docs } from "./(site)/docs/content";
import { publicPages, siteOrigin } from "@/lib/seo";

export default function sitemap(): MetadataRoute.Sitemap {
  const paths = [...Object.keys(publicPages), ...docs.map(({ slug }) => `/docs/${slug}`)];
  return paths.map(path => ({ url: new URL(path, siteOrigin).href }));
}
