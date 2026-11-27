import Image from "next/image";

export function Logo({ variant = "cream", height = 30 }: { variant?: "forest" | "cream"; height?: number }) {
  return <Image src={`/hedvi-lockup-${variant}.svg`} alt="Hedvi" width={Math.round((height * 355) / 110)} height={height} priority />;
}
