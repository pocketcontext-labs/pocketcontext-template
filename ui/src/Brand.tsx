import logo from "./assets/brand/logo.svg?no-inline";

/** TemplateContext mark and wordmark. Rename both when deriving a product. */
export default function Brand() {
  return (
    <div className="product-brand">
      <img src={logo} width="32" height="32" alt="" />
      <span>TemplateContext</span>
    </div>
  );
}
