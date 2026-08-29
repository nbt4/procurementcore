import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import './theme.css'
import './index.css'
import './cores-theme.css'
import { appBasePath } from './lib/app-paths'

document.addEventListener('wheel', (event) => {
  const target = event.target
  if (target instanceof HTMLInputElement && target.type === 'number' && document.activeElement === target) {
    target.blur()
  }
}, { capture: true, passive: true })

createRoot(document.getElementById('root')!).render(<StrictMode><BrowserRouter basename={appBasePath || undefined}><App /></BrowserRouter></StrictMode>)
