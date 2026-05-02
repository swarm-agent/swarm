# Reusable lab outline

Source pattern checked from `swarm-web`:
- `frontend/src/pages/news-mocks/01.astro` through `10.astro`: one standalone review surface per option, realistic content, local styles, no product app shell dependency.
- `frontend/src/components/react/HeroPicker.tsx`: one page with a fixed bottom selector and indexed variants.
- `frontend/src/components/react/SublinePicker.tsx`: minimal variant picker state, no surrounding application flow.

## Contract for Swarm labs

Use this when a parallel agent needs to review UI alternatives without touching production behavior.

1. Put lab-only React helpers under `web/src/lab/`.
2. Put feature-specific lab files next to the feature only when needed.
3. Render exactly one lab surface at `/lab` by temporarily bypassing the normal app router in `web/src/main.tsx`.
4. Do not route through `app/router.tsx`, `DesktopVaultShell`, onboarding, vault, workspace loading, or backend state.
5. Keep production components unchanged unless the task explicitly asks to implement a chosen result.
6. Use realistic in-product content inside variants.
7. Keep labels/instructions outside the variant surface; the review target should look like product UI.
8. Use a fixed bottom selector for switching variants, copied from the `swarm-web` picker pattern.
9. Commit the reusable outline separately from any experimental variants.
10. Delete or replace the feature-specific lab after review; keep the generic outline reusable.

## Reusable React shell

Use `web/src/lab/lab-outline.tsx` for the variant picker shell:

```tsx
import { LabOutlinePage } from './lab/lab-outline'

const variants = [
  { id: '01', render: () => <VariantOne /> },
  { id: '02', render: () => <VariantTwo /> },
]

export function FeatureLabPage() {
  return <LabOutlinePage variants={variants} />
}
```

## Temporary `/lab` bypass

Add this only while a lab is active:

```tsx
function App() {
  if (window.location.pathname === '/lab') {
    return <FeatureLabPage />
  }

  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}
```

Remove the bypass when the lab is no longer needed.
