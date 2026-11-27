import Image from "next/image";

export function DocFigure({ name, alt, caption }: { name: string; alt: string; caption: string }) {
  return <figure style={{ margin: "28px 0" }}>
    <Image src={`/docs/${name}.svg`} alt={alt} width={1080} height={370} style={{ width: "100%", height: "auto" }} />
    <figcaption className="small muted" style={{ marginTop: 8 }}>{caption}</figcaption>
  </figure>;
}
