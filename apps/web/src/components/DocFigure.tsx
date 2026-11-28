import Image from "next/image";

export function DocFigure({ name, alt, caption }: { name: string; alt: string; caption: string }) {
  return <figure style={{ margin: "28px 0" }}>
    <a href={`/docs/${name}.svg`} target="_blank" rel="noopener noreferrer" aria-label={`Open full-size diagram: ${alt}`}>
      <Image src={`/docs/${name}.svg`} alt={alt} width={1080} height={370} style={{ width: "100%", height: "auto" }} />
    </a>
    <figcaption className="small muted" style={{ marginTop: 8 }}>{caption} <a href={`/docs/${name}.svg`} target="_blank" rel="noopener noreferrer">View full-size diagram</a></figcaption>
  </figure>;
}
