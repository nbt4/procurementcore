import { ReactNode } from 'react'
import { X } from 'lucide-react'

export function Button({ children, variant = '', className = '', ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary'|'ghost'|'danger'|'' }) {
  return <button className={`btn ${variant} ${className}`} {...props}>{children}</button>
}

export function Modal({ title, children, onClose, wide = false, footer }: { title:string; children:ReactNode; onClose:()=>void; wide?:boolean; footer?:ReactNode }) {
  return <div className="modal-backdrop" onMouseDown={(e)=>{if(e.currentTarget===e.target)onClose()}}>
    <section className={`modal ${wide?'wide':''}`} role="dialog" aria-modal="true" aria-label={title}>
      <header className="modal-header"><h3>{title}</h3><Button className="icon" variant="ghost" onClick={onClose} aria-label="Schließen"><X size={18}/></Button></header>
      <div className="modal-body">{children}</div>{footer&&<footer className="modal-footer">{footer}</footer>}
    </section>
  </div>
}

export function Field({ label, children, full=false }: { label:string; children:ReactNode; full?:boolean }) { return <div className={`field ${full?'full':''}`}><label>{label}</label>{children}</div> }
export function Badge({ children, tone='' }: { children:ReactNode; tone?:string }) { return <span className={`badge ${tone}`}>{children}</span> }
export function Empty({ children }: { children:ReactNode }) { return <div className="empty">{children}</div> }
