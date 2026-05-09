[Home](https://tailwindcss.com/plus)

[Introducing Oatmeal\\
Our new multi-theme marketing site kit](https://tailwindcss.com/plus/kits/oatmeal)

``[UI Blocks](https://tailwindcss.com/plus/ui-blocks) [Templates](https://tailwindcss.com/plus/templates) [UI Kit](https://tailwindcss.com/plus/ui-kit)

[Sign in](https://tailwindcss.com/plus/login) [Get full access](https://tailwindcss.com/plus#pricing)

- [Marketing](https://tailwindcss.com/plus/ui-blocks/marketing)
- [Application UI](https://tailwindcss.com/plus/ui-blocks/application-ui)
- [Ecommerce](https://tailwindcss.com/plus/ui-blocks/ecommerce)
- [Documentation](https://tailwindcss.com/plus/ui-blocks/documentation)
- ### Getting started

- [Getting set up](https://tailwindcss.com/plus/ui-blocks/documentation)
- [Using HTML](https://tailwindcss.com/plus/ui-blocks/documentation/using-html)
- [Using React](https://tailwindcss.com/plus/ui-blocks/documentation/using-react)
- [Using Vue](https://tailwindcss.com/plus/ui-blocks/documentation/using-vue)
- [Assets](https://tailwindcss.com/plus/ui-blocks/documentation/assets)
- ### Elements

- [Introduction](https://tailwindcss.com/plus/ui-blocks/documentation/elements)
- [Autocomplete](https://tailwindcss.com/plus/ui-blocks/documentation/autocomplete)
- [Command palette](https://tailwindcss.com/plus/ui-blocks/documentation/command-palette)
- [Copy button](https://tailwindcss.com/plus/ui-blocks/documentation/copy-button)
- [Dialog](https://tailwindcss.com/plus/ui-blocks/documentation/dialog)
- [Disclosure](https://tailwindcss.com/plus/ui-blocks/documentation/disclosure)
- [Dropdown menu](https://tailwindcss.com/plus/ui-blocks/documentation/dropdown-menu)
- [Popover](https://tailwindcss.com/plus/ui-blocks/documentation/popover)
- [Select](https://tailwindcss.com/plus/ui-blocks/documentation/select)
- [Tabs](https://tailwindcss.com/plus/ui-blocks/documentation/tabs)

5-day Mini-Course

### Build UIs that don’t suck.

**Short, tactical video lessons** from the creator of Tailwind CSS, delivered directly to your inbox **every day for a week**.

[Get the free course](https://tailwindcss.com/build-uis-that-dont-suck)

[UI Blocks](https://tailwindcss.com/plus/ui-blocks)

[Documentation](https://tailwindcss.com/plus/ui-blocks/documentation)

[UI Blocks](https://tailwindcss.com/plus/ui-blocks)

/ Docs

# Getting set up

## Requirements

All of the components in Tailwind Plus are designed for the latest version of Tailwind CSS, which is currently Tailwind CSS v4.2. To make sure that you are on the latest version of Tailwind, update via npm:

```bash
npm install tailwindcss@latest
```

If you're new to Tailwind CSS, you'll want to [read the Tailwind CSS documentation](https://tailwindcss.com/docs) as well to get the most out of Tailwind Plus.

## Add the Inter font family

We've used [Inter](https://rsms.me/inter) for all of the Tailwind Plus examples because it's a beautiful font for UI design and is completely open-source and free. Using a custom font is nice because it allows us to make the components look the same on all browsers and operating systems.

You can use any font you want in your own project of course, but if you'd like to use Inter, the easiest way is to first add it via the CDN:

```html
<link rel="stylesheet" href="https://rsms.me/inter/inter.css" />
```

Then add "InterVariable" to your "sans" font family in your Tailwind theme:

```css
@theme {
  --font-sans: InterVariable, sans-serif;
  --font-sans--font-feature-settings: 'cv02', 'cv03', 'cv04', 'cv11';
}
```

If you're still on Tailwind CSS v3.x, you can do this in your`tailwind.config.js` file:

```js
const defaultTheme = require('tailwindcss/defaultTheme')

module.exports = {
  theme: {
    extend: {
      fontFamily: {
        sans: ['InterVariable', ...defaultTheme.fontFamily.sans],
      },
    },
  },
  // ...
}
```

## Dark mode support

If you're using dark mode components, add the `dark:scheme-dark` class to your`<html>` element to ensure that the browser renders scrollbars and other native UIs correctly in dark mode. Also include the `dark:bg-gray-950` class to provide a dark background for the entire page:

```html
<html class="bg-white dark:bg-gray-950 scheme-light dark:scheme-dark">
```

## On this page

- [Requirements](https://tailwindcss.com/plus/ui-blocks/documentation#requirements)
- [Add the Inter font family](https://tailwindcss.com/plus/ui-blocks/documentation#add-the-inter-font-family)
- [Dark mode support](https://tailwindcss.com/plus/ui-blocks/documentation#dark-mode-support)

### Marketing

- [Hero Sections](https://tailwindcss.com/plus/ui-blocks/marketing/sections/heroes)
- [Feature Sections](https://tailwindcss.com/plus/ui-blocks/marketing/sections/feature-sections)
- [Pricing Sections](https://tailwindcss.com/plus/ui-blocks/marketing/sections/pricing)
- [Header Sections](https://tailwindcss.com/plus/ui-blocks/marketing/sections/header)
- [Newsletter Sections](https://tailwindcss.com/plus/ui-blocks/marketing/sections/newsletter-sections)
- [Testimonials](https://tailwindcss.com/plus/ui-blocks/marketing/sections/testimonials)
- [Team Sections](https://tailwindcss.com/plus/ui-blocks/marketing/sections/team-sections)
- [Content Sections](https://tailwindcss.com/plus/ui-blocks/marketing/sections/content-sections)
- [Logo Clouds](https://tailwindcss.com/plus/ui-blocks/marketing/sections/logo-clouds)
- [FAQs](https://tailwindcss.com/plus/ui-blocks/marketing/sections/faq-sections)
- [Footers](https://tailwindcss.com/plus/ui-blocks/marketing/sections/footers)
- [Headers](https://tailwindcss.com/plus/ui-blocks/marketing/sections/header)
- [Flyout Menus](https://tailwindcss.com/plus/ui-blocks/marketing/elements/flyout-menus)
- [Banners](https://tailwindcss.com/plus/ui-blocks/marketing/elements/banners)
- [Browse all →](https://tailwindcss.com/plus/ui-blocks/marketing)

### Application UI

- [Tables](https://tailwindcss.com/plus/ui-blocks/application-ui/lists/tables)
- [Feeds](https://tailwindcss.com/plus/ui-blocks/application-ui/lists/feeds)
- [Form Layouts](https://tailwindcss.com/plus/ui-blocks/application-ui/forms/form-layouts)
- [Select Menus](https://tailwindcss.com/plus/ui-blocks/application-ui/forms/select-menus)
- [Radio Groups](https://tailwindcss.com/plus/ui-blocks/application-ui/forms/radio-groups)
- [Checkboxes](https://tailwindcss.com/plus/ui-blocks/application-ui/forms/checkboxes)
- [Comboboxes](https://tailwindcss.com/plus/ui-blocks/application-ui/forms/comboboxes)
- [Navbars](https://tailwindcss.com/plus/ui-blocks/application-ui/navigation/navbars)
- [Pagination](https://tailwindcss.com/plus/ui-blocks/application-ui/navigation/pagination)
- [Sidebar Navigation](https://tailwindcss.com/plus/ui-blocks/application-ui/navigation/sidebar-navigation)
- [Command Palettes](https://tailwindcss.com/plus/ui-blocks/application-ui/navigation/command-palettes)
- [Modals](https://tailwindcss.com/plus/ui-blocks/application-ui/overlays/modal-dialogs)
- [Dropdowns](https://tailwindcss.com/plus/ui-blocks/application-ui/elements/dropdowns)
- [Buttons](https://tailwindcss.com/plus/ui-blocks/application-ui/elements/buttons)
- [Browse all →](https://tailwindcss.com/plus/ui-blocks/application-ui)

### Ecommerce

- [Product Overviews](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/product-overviews)
- [Product Lists](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/product-lists)
- [Category Previews](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/category-previews)
- [Shopping Carts](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/shopping-carts)
- [Category Filters](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/category-filters)
- [Product Quickviews](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/product-quickviews)
- [Product Features](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/product-features)
- [Store Navigation](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/store-navigation)
- [Promo Sections](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/promo-sections)
- [Checkout Forms](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/checkout-forms)
- [Reviews](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/reviews)
- [Order Summaries](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/order-summaries)
- [Order History](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/order-history)
- [Incentives](https://tailwindcss.com/plus/ui-blocks/ecommerce/components/incentives)
- [Browse all →](https://tailwindcss.com/plus/ui-blocks/ecommerce)

### Templates & UI Kits

- [Catalyst UI Kit](https://tailwindcss.com/plus/templates/catalyst)
- [Personal Website Template](https://tailwindcss.com/plus/templates/spotlight)
- [Landing Page Template](https://tailwindcss.com/plus/templates/salient)
- [API Reference Template](https://tailwindcss.com/plus/templates/protocol)
- [Changelog Template](https://tailwindcss.com/plus/templates/commit)
- [Info Product Template](https://tailwindcss.com/plus/templates/primer)
- [Agency Template](https://tailwindcss.com/plus/templates/studio)
- [Podcast Template](https://tailwindcss.com/plus/templates/transmit)
- [App Marketing Template](https://tailwindcss.com/plus/templates/pocket)
- [Documentation Template](https://tailwindcss.com/plus/templates/syntax)
- [Conference Template](https://tailwindcss.com/plus/templates/keynote)
- [Browse all →](https://tailwindcss.com/plus/templates)

© 2026 Tailwind Labs Inc. All rights reserved.

[Privacy policy](https://tailwindcss.com/plus/privacy-policy)

[Changelog](https://tailwindcss.com/plus/changelog)