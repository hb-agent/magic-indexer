import type { Metadata } from "next";
import { Inter, EB_Garamond } from "next/font/google";
import Image from "next/image";
import { Header } from "@/components/layout/Header";
import { Providers } from "@/components/Providers";
import "./globals.css";
const inter = Inter({ variable: "--font-inter", subsets: ["latin"] });
const garamond = EB_Garamond({ variable: "--font-garamond", subsets: ["latin"], weight: ["400", "500", "600", "700"] });
export const metadata: Metadata = { title: "Hyperindex", description: "AT Protocol AppView Server", icons: { icon: "/gainforest-logo.png", apple: "/gainforest-logo.png" } };
export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <head><script dangerouslySetInnerHTML={{ __html: `(function(){var t=localStorage.getItem('theme');if(!t)t=window.matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light';document.documentElement.setAttribute('data-theme',t)})()` }} /></head>
      <body className={`${inter.variable} ${garamond.variable} antialiased`} style={{ backgroundColor: 'var(--bg-canvas)', color: 'var(--fg-secondary)' }}>
        <Providers>
          <div className="relative min-h-screen flex flex-col">
            <Header />
            <main className="relative flex-1 w-full max-w-4xl mx-auto px-4 sm:px-8 pb-8">{children}</main>
            <footer className="relative py-6 mt-auto"><div className="max-w-4xl mx-auto px-4 sm:px-8"><a href="https://gainforest.earth" target="_blank" rel="noopener noreferrer" className="flex items-center justify-center gap-1.5 hover:opacity-80 transition-opacity"><span className="text-[11px] tracking-wide" style={{ color: 'var(--fg-muted)' }}>Made by</span><Image src="/gainforest-logo.png" alt="GainForest" width={14} height={14} className="inline-block" /><span className="text-[11px] font-medium tracking-wide" style={{ color: 'var(--fg-primary)' }}>GainForest</span></a></div></footer>
          </div>
        </Providers>
      </body>
    </html>
  );
}
