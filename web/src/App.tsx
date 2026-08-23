import { createContext, useContext, useEffect, useState } from 'react'
import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { BellRing, Boxes, Building2, ClipboardCheck, FileDown, Gauge, Home, Menu, ShoppingCart, Users } from 'lucide-react'
import { api, apiBase } from './lib/api'
import type { Branding, User } from './lib/types'
import DashboardPage from './pages/DashboardPage'
import CatalogPage from './pages/CatalogPage'
import SuppliersPage from './pages/SuppliersPage'
import AlertsPage from './pages/AlertsPage'
import RequisitionsPage from './pages/RequisitionsPage'
import OrdersPage from './pages/OrdersPage'

type AppContextValue = { user: User; refreshKey:number; refresh:()=>void; notify:(message:string)=>void }
const AppContext = createContext<AppContextValue | null>(null)
export const useApp = () => useContext(AppContext)!

const navigation = [
  { to:'/', label:'Übersicht', icon:Gauge, mobile:true },
  { to:'/catalog', label:'Katalog', icon:Boxes, mobile:true },
  { to:'/suppliers', label:'Lieferanten', icon:Building2 },
  { to:'/alerts', label:'Tiefpreis', icon:BellRing, mobile:true },
  { to:'/requisitions', label:'Bedarfe', icon:ClipboardCheck, mobile:true },
  { to:'/orders', label:'Bestellungen', icon:ShoppingCart, mobile:true },
]

function Shell({ user, branding, children }:{ user:User; branding:Branding|null; children:React.ReactNode }) {
  const location = useLocation()
  const active = navigation.find(item => item.to === '/' ? location.pathname === '/' : location.pathname.startsWith(item.to))
  return <div className="shell">
    <aside className="sidebar">
      <a className="brand" href={window.__DASHBOARD_URL__ || '/'}>
        {branding?.sidebarLogo ? <img src={branding.sidebarLogo} alt="ProcurementCore"/> : <><span className="brand-mark">P</span><span><span className="brand-title">ProcurementCore</span><span className="brand-subtitle">Einkauf</span></span></>}
      </a>
      <nav className="nav">{navigation.map(({to,label,icon:Icon})=><NavLink key={to} to={to} end={to==='/' }><Icon size={18}/><span>{label}</span></NavLink>)}</nav>
      <div className="sidebar-footer">
        <a className="btn ghost" style={{width:'100%',marginBottom:'.5rem'}} href={`${apiBase}/export/spend.csv`}><FileDown size={16}/> Spend exportieren</a>
        <a className="btn ghost" style={{width:'100%',marginBottom:'.55rem'}} href={window.__DASHBOARD_URL__ || '/'}><Home size={16}/> Cores Dashboard</a>
        <div className="user"><div className="avatar">{user.username.slice(0,1).toUpperCase()}</div><div className="user-meta"><div className="user-name">{user.username}</div><div className="user-role">{user.isAdmin?'Einkaufsadministration':'Anforderer'}</div></div></div>
      </div>
    </aside>
    <header className="mobile-header"><strong>ProcurementCore</strong><span className="badge pink"><Menu size={13}/>{active?.label}</span></header>
    <main className="main"><header className="topbar"><h1>{active?.label || 'ProcurementCore'}</h1><div className="top-actions"><span className="badge"><Users size={13}/>{user.username}</span></div></header>{children}</main>
    <nav className="mobile-tabs">{navigation.filter(n=>n.mobile).map(({to,label,icon:Icon})=><NavLink key={to} to={to} end={to==='/' }><Icon size={19}/><span>{label}</span></NavLink>)}</nav>
  </div>
}

export default function App() {
  const [user,setUser]=useState<User|null>(null), [branding,setBranding]=useState<Branding|null>(null), [loading,setLoading]=useState(true), [refreshKey,setRefreshKey]=useState(0), [toast,setToast]=useState('')
  useEffect(()=>{ Promise.all([api<User>('/me'),api<Branding>('/branding').catch(()=>null)]).then(([u,b])=>{setUser(u);setBranding(b); if(b?.faviconPath){let link=document.querySelector<HTMLLinkElement>("link[rel~='icon']");if(!link){link=document.createElement('link');link.rel='icon';document.head.appendChild(link)}link.href=b.faviconPath}}).finally(()=>setLoading(false)) },[])
  useEffect(()=>{ if(!toast)return; const id=setTimeout(()=>setToast(''),3200); return()=>clearTimeout(id) },[toast])
  if(loading) return <div className="empty" style={{paddingTop:'30vh'}}>ProcurementCore wird geladen …</div>
  if(!user) return <Navigate to="/"/>
  const value={user,refreshKey,refresh:()=>setRefreshKey(k=>k+1),notify:setToast}
  return <AppContext.Provider value={value}><Shell user={user} branding={branding}><Routes>
    <Route path="/" element={<DashboardPage/>}/><Route path="/catalog" element={<CatalogPage/>}/><Route path="/suppliers" element={<SuppliersPage/>}/><Route path="/alerts" element={<AlertsPage/>}/><Route path="/requisitions" element={<RequisitionsPage/>}/><Route path="/orders" element={<OrdersPage/>}/><Route path="*" element={<Navigate to="/"/>}/>
  </Routes></Shell>{toast&&<div className="toast">{toast}</div>}</AppContext.Provider>
}
