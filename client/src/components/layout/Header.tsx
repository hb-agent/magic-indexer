'use client'
import { useState, useRef, useEffect } from 'react'
import Link from 'next/link'
import Image from 'next/image'
import { usePathname } from 'next/navigation'
import { useAuth } from '@/lib/auth'
import { ThemeToggle } from './ThemeToggle'
const navLinks = [{ href: '/', label: 'Dashboard' }, { href: '/lexicons', label: 'Lexicons' }, { href: '/backfill', label: 'Backfill' }, { href: '/docs', label: 'API Docs' }]
export function Header() {
  const pathname = usePathname()
  const { isAuthenticated, isLoading, session, login, logout } = useAuth()
  const [showDropdown, setShowDropdown] = useState(false)
  const [showLoginModal, setShowLoginModal] = useState(false)
  const [handle, setHandle] = useState('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [error, setError] = useState('')
  const dropdownRef = useRef<HTMLDivElement>(null)
  const isActive = (href: string) => { if (href === '/') return pathname === '/'; return pathname.startsWith(href) }
  useEffect(() => { if (!showDropdown) return; const h = (e: MouseEvent) => { if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) setShowDropdown(false) }; document.addEventListener('mousedown', h); return () => document.removeEventListener('mousedown', h) }, [showDropdown])
  const handleLogin = async (e: React.FormEvent) => { e.preventDefault(); if (!handle.trim()) return; setIsSubmitting(true); setError(''); try { await login(handle.trim()) } catch (err) { setError(err instanceof Error ? err.message : 'Login failed'); setIsSubmitting(false) } }
  const handleLogout = async () => { setShowDropdown(false); await logout() }
  return (<>
    <nav className="sticky top-0 z-50 border-b" style={{ backgroundColor: 'var(--navbar-bg)', borderColor: 'var(--navbar-border)', backdropFilter: 'blur(12px)', WebkitBackdropFilter: 'blur(12px)' }}>
      <div className="max-w-4xl mx-auto px-4 sm:px-8"><div className="flex items-center h-16">
        <Link href="/" className="flex items-center gap-2.5"><Image src="/logo.png" alt="Hyperindex" width={20} height={20} className="opacity-80" /><span className="text-lg font-semibold" style={{ color: 'var(--fg-primary)', letterSpacing: '-0.01em' }}>hi</span></Link>
        <div className="hidden sm:flex items-center gap-1 ml-8">{navLinks.map(({ href, label }) => (<Link key={href} href={href} className="px-3 py-1.5 text-sm rounded-sm transition-colors duration-150" style={{ color: isActive(href) ? 'var(--fg-primary)' : 'var(--fg-muted)', fontWeight: isActive(href) ? 500 : 400 }}>{label}</Link>))}</div>
        <div className="flex items-center gap-2 ml-auto">
          <ThemeToggle />
          <div className="relative" ref={dropdownRef}>
            {isLoading ? (<div className="w-8 h-8 rounded-full skeleton" />) : (<button onClick={() => setShowDropdown(!showDropdown)} className="flex items-center cursor-pointer">{isAuthenticated && session ? (session.avatar ? (<Image src={session.avatar} alt="" width={30} height={30} className="rounded-full" />) : (<div className="w-[30px] h-[30px] rounded-full flex items-center justify-center text-sm font-medium" style={{ backgroundColor: 'var(--bg-raised)', color: 'var(--fg-primary)' }}>{(session.displayName || session.handle).charAt(0).toUpperCase()}</div>)) : (<span className="text-sm" style={{ color: 'var(--fg-muted)' }}>Sign in</span>)}</button>)}
            {showDropdown && (<div className="absolute right-0 top-full mt-2 w-48 rounded-sm py-2 z-50" style={{ backgroundColor: 'var(--bg-elevated)', border: '1px solid var(--border-default)', boxShadow: 'var(--shadow-lg)' }}>
              {isAuthenticated && session && (<div className="px-4 py-2 mb-1" style={{ borderBottom: '1px solid var(--border-subtle)' }}><p className="text-sm font-medium truncate" style={{ color: 'var(--fg-primary)' }}>{session.displayName || session.handle}</p><p className="text-xs truncate" style={{ color: 'var(--fg-muted)' }}>@{session.handle}</p></div>)}
              <div className="sm:hidden py-1 mb-1" style={{ borderBottom: '1px solid var(--border-subtle)' }}>{navLinks.map(({ href, label }) => (<Link key={href} href={href} onClick={() => setShowDropdown(false)} className="block px-4 py-2 text-sm" style={{ color: isActive(href) ? 'var(--fg-primary)' : 'var(--fg-secondary)' }}>{label}</Link>))}</div>
              <div className="py-1"><Link href="/settings" onClick={() => setShowDropdown(false)} className="block px-4 py-2 text-sm" style={{ color: 'var(--fg-secondary)' }}>Settings</Link><a href="/graphiql" target="_blank" rel="noopener noreferrer" onClick={() => setShowDropdown(false)} className="flex items-center justify-between px-4 py-2 text-sm" style={{ color: 'var(--fg-secondary)' }}>GraphiQL<svg className="w-3 h-3" style={{ color: 'var(--fg-muted)' }} fill="none" viewBox="0 0 24 24" strokeWidth={2} stroke="currentColor"><path strokeLinecap="round" strokeLinejoin="round" d="m4.5 19.5 15-15m0 0H8.25m11.25 0v11.25" /></svg></a></div>
              <div className="mt-1 pt-1" style={{ borderTop: '1px solid var(--border-subtle)' }}>{isAuthenticated ? (<button onClick={handleLogout} className="block w-full text-left px-4 py-2 text-sm cursor-pointer" style={{ color: 'var(--fg-muted)' }}>Sign out</button>) : (<button onClick={() => { setShowDropdown(false); setShowLoginModal(true) }} className="block w-full text-left px-4 py-2 text-sm cursor-pointer" style={{ color: 'var(--fg-primary)' }}>Sign in</button>)}</div>
            </div>)}
          </div>
        </div>
      </div></div>
    </nav>
    {showLoginModal && (<div className="fixed inset-0 z-50 flex items-start justify-center pt-[20vh]" onClick={() => setShowLoginModal(false)}><div className="absolute inset-0" style={{ backgroundColor: 'rgba(0,0,0,0.25)', backdropFilter: 'blur(8px)' }} /><div className="relative w-full max-w-sm mx-4 p-8" style={{ backgroundColor: 'var(--bg-elevated)', border: '1px solid var(--border-default)', borderRadius: '20px', boxShadow: 'var(--shadow-lg)' }} onClick={(e) => e.stopPropagation()}><h2 className="font-[family-name:var(--font-garamond)] text-xl mb-1" style={{ color: 'var(--fg-primary)' }}>Sign in with ATProto</h2><p className="text-sm mb-5" style={{ color: 'var(--fg-muted)' }}>Enter your Bluesky handle to connect.</p><form onSubmit={handleLogin}><label htmlFor="auth-handle" className="block text-sm mb-1.5" style={{ color: 'var(--fg-secondary)' }}>Handle</label><input id="auth-handle" type="text" value={handle} onChange={(e) => setHandle(e.target.value)} placeholder="alice.bsky.social" disabled={isSubmitting} autoFocus className="w-full text-sm disabled:opacity-50" style={{ height: '56px', padding: '0 20px', backgroundColor: 'var(--bg-elevated)', border: '1.5px solid var(--border-default)', borderRadius: '8px', color: 'var(--fg-primary)', outline: 'none' }} /><p className="text-xs mt-1.5" style={{ color: 'var(--fg-muted)' }}>Just a username? We&apos;ll add .bsky.social for you.</p>{error && <p className="text-sm mt-2" style={{ color: 'var(--color-error)' }}>{error}</p>}<div className="flex gap-2 mt-5"><button type="button" onClick={() => setShowLoginModal(false)} disabled={isSubmitting} className="flex-1 px-3 py-2.5 text-sm rounded-sm disabled:opacity-50 cursor-pointer" style={{ color: 'var(--fg-secondary)', backgroundColor: 'var(--bg-raised)', border: '1px solid var(--border-default)' }}>Cancel</button><button type="submit" disabled={isSubmitting || !handle.trim()} className="flex-1 px-3 py-2.5 text-sm font-medium rounded-full disabled:opacity-50 cursor-pointer" style={{ backgroundColor: 'var(--btn-primary-bg)', color: 'var(--btn-primary-fg)' }}>{isSubmitting ? 'Connecting...' : 'Connect'}</button></div></form></div></div>)}
  </>)
}
