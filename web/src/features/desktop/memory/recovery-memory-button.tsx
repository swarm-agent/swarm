import { useState } from 'react'
import { Button } from '../../../components/ui/button'
import { MemoryModal } from './memory-page'

// Nothing is copied from blocker evidence automatically: it may contain private
// diagnostics. The user authors and reviews reusable guidance before saving.
export function RecoveryMemoryButton() {
 const [open, setOpen] = useState(false)
 return <><Button variant="ghost" size="sm" onClick={() => setOpen(true)}>Review recovery guidance to remember</Button>{open && <MemoryModal recoveryText="" onClose={() => setOpen(false)} />}</>
}
