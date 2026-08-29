import { useEffect, useState } from 'react'
import {
  BellRing, Boxes, Building2, CheckCircle2, ClipboardCheck, Coins,
  FileCheck2, PackageCheck, RefreshCw, ShoppingCart,
} from 'lucide-react'
import { api, dateTime, euro } from '../lib/api'
import type { Dashboard } from '../lib/types'
import { useApp } from '../App'
import { suiteDateLabel, suiteGreeting } from '../lib/cores-design'

export default function DashboardPage() {
  const { user, refreshKey, refresh } = useApp()
  const [data, setData] = useState<Dashboard | null>(null)
  const [loading, setLoading] = useState(true)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)

  useEffect(() => {
    setLoading(true)
    api<Dashboard>('/dashboard')
      .then((result) => {
        setData(result)
        setLastUpdated(new Date())
      })
      .finally(() => setLoading(false))
  }, [refreshKey])

  const metrics = [
    { label: 'Offene Freigaben', value: data?.pendingApprovals ?? 0, detail: 'Eingereichte Bedarfe', icon: ClipboardCheck },
    { label: 'Ausgelöste Tiefpreise', value: data?.triggeredAlerts ?? 0, detail: 'Aktive Preisalarme', icon: BellRing },
    { label: 'Bestellvolumen', value: euro(data?.spend.cents), detail: 'Gesamt ohne Stornos', icon: Coins },
    { label: 'Realisierte Einsparung', value: euro(data?.savings.cents), detail: 'Gegenüber Schätzung', icon: CheckCircle2 },
  ]

  return (
    <div className="content suite-dashboard">
      <header className="suite-dashboard-header">
        <div className="suite-dashboard-heading">
          <p className="suite-dashboard-eyebrow"><span className="suite-dashboard-eyebrow-dot" />{suiteDateLabel()}</p>
          <h1 className="suite-dashboard-title">{suiteGreeting(user)}</h1>
          <p className="suite-dashboard-subtitle">Bedarfe, Bezugsquellen und Bestellungen auf einem Stand.</p>
        </div>
        <div className="suite-dashboard-actions">
          {lastUpdated && <span className="suite-dashboard-timestamp">Aktualisiert {lastUpdated.toLocaleTimeString('de-DE', { hour: '2-digit', minute: '2-digit' })}</span>}
          <button type="button" className="suite-button" onClick={refresh} disabled={loading}>
            <RefreshCw size={16} className={loading ? 'spin' : ''} />Aktualisieren
          </button>
        </div>
      </header>

      <section className="suite-kpi-grid" aria-label="Einkaufskennzahlen">
        {metrics.map(({ label, value, detail, icon: Icon }) => (
          <article className="suite-kpi-card" key={label}>
            <div className="suite-kpi-label"><Icon size={15} />{label}</div>
            <div className="suite-kpi-value">{value}</div>
            <div className="suite-kpi-detail">{detail}</div>
          </article>
        ))}
      </section>

      <div className="grid dashboard-lower">
        <section className="panel">
          <header className="panel-header">
            <div><h2>Jetzt bearbeiten</h2><p>Nach Auswirkung auf den Einkauf priorisiert</p></div>
            <span className="panel-reference">{(data?.pendingApprovals ?? 0) + (data?.triggeredAlerts ?? 0)} offen</span>
          </header>
          <div className="procurement-priorities">
            <a href="/requisitions" className="procurement-priority">
              <ClipboardCheck size={18} /><span><strong>Freigaben entscheiden</strong><small>Eingereichte Bedarfe prüfen und freigeben</small></span><b>{data?.pendingApprovals ?? 0}</b>
            </a>
            <a href="/alerts" className="procurement-priority">
              <BellRing size={18} /><span><strong>Tiefpreise prüfen</strong><small>Ausgelöste Preisalarme bewerten</small></span><b>{data?.triggeredAlerts ?? 0}</b>
            </a>
          </div>
        </section>

        <section className="panel">
          <header className="panel-header"><div><h2>Schnellstart</h2><p>Häufige Einkaufsaktionen</p></div></header>
          <div className="procurement-quick-grid">
            <a className="suite-button suite-button--primary" href="/requisitions">Bedarf erfassen</a>
            <a className="suite-button" href="/catalog">Katalog öffnen</a>
            <a className="suite-button" href="/suppliers">Lieferanten</a>
            <a className="suite-button" href="/orders">Bestellungen</a>
          </div>
          <div className="procurement-inventory-meta">
            <span><Building2 size={15} />{data?.preferredSuppliers ?? 0} bevorzugte Lieferanten</span>
            <span><Boxes size={15} />{data?.activeProducts ?? 0} aktive Katalogartikel</span>
          </div>
        </section>
      </div>

      <section className="panel">
        <header className="panel-header"><div><h2>Beschaffungsablauf</h2><p>Vom Bedarf bis zum gebuchten Eingang</p></div><span className="panel-reference">5 Schritte</span></header>
        <div className="panel-body workflow">
          {[
            [ClipboardCheck, 'Bedarf', 'Erfassen'],
            [FileCheck2, 'Freigabe', 'Prüfen'],
            [Building2, 'Sourcing', 'Vergleichen'],
            [ShoppingCart, 'Bestellung', 'Auslösen'],
            [PackageCheck, 'Eingang', 'Verbuchen'],
          ].map(([IconValue, title, subtitle], index) => {
            const Icon = IconValue as typeof ClipboardCheck
            return <div className="workflow-step" key={title as string}><span className="workflow-index">0{index + 1}</span><Icon size={17} /><strong>{title as string}</strong><small>{subtitle as string}</small></div>
          })}
        </div>
      </section>

      <section className="panel">
        <header className="panel-header"><div><h2>Letzte Aktivitäten</h2><p>Änderungen im Einkauf</p></div></header>
        <div className="panel-body activity-list">
          {data?.recentActivity.length
            ? data.recentActivity.map((row) => <div className="activity" key={row.id}><span className="activity-dot" /><div><strong>{row.action.replaceAll('_', ' ')} · {row.username}</strong><p>{row.entityType} #{row.entityId} · {dateTime(row.createdAt)}</p></div></div>)
            : <p className="muted-copy">Noch keine Aktivitäten.</p>}
        </div>
      </section>
    </div>
  )
}
