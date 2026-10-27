// web/src/app.js —— 前端全部逻辑（源文件，含 I18N_EN 字典）。
// 注意：不要直接改 web/index.html，它由 python tools/web_build.py 从
//   web/src/index.tpl.html + app.css + app.js 拼出来。
// 改完三件套后跑一次：python tools/web_build.py（i18n_build.py 会自动代跑）。

const $ = s => document.querySelector(s);
window.__errs = []; onerror = (m,u,l) => { window.__errs.push(m + '@' + l); };
let cfg = null, loaded = false, dirty = false, saveTimer = null, busyNow = '';
/* 设置页的模块级状态。**必须声明在这里**：goto() 在页面加载末尾会被
   ekApplyHash() 立即调用一次（深链 #settings 会直接进设置页），而 goto 里会调
   ekSettingsEnter() → 读 EK_SET。写成 function 声明能提升，const 不能——
   放到文件末尾就会撞 TDZ：Uncaught ReferenceError: Cannot access 'EK_SET'
   before initialization，表现为「设置页一片空白、按钮全无反应」。
   （同类坑在个人中心/桌宠那两个 IIFE 上踩过一次，node --check 查不出来。） */
const EK_SET = {tab:'base', base:null, dev:null, ev:null, evTimer:null, aiQuirk:null};

/* ---------- 应用图标（由 tools/make_icon.py 从 cici.png 生成，base64 内嵌保持单文件） ---------- */
const APP_ICON = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAYAAACqaXHeAAAmMUlEQVR42m2beZhcV3nmf2e5S1V1Ve/d6k1ba5csW953Y/ACNuDERAbswDCESTIOScgyk0ySGaE8yRMmIQthkoGEEBggMbbBgLEdvGEbb8iyZUm2ZO2ypG71vlTXeu8958wft7rVLbv+qfXeuuc73/e+77dcwbs8drBDfp6dToADxGO9t75PSfch49zV1rnVQtDshFBA+gvAOosUsvGRWziXQOCcA5G+nn84XOOdaLwWIAC3+OjG8Y0zCpGea/45/Z75sxoBsxJxXArxorHi4fcP/+QpAc6B+Dw7xE522vPXKs7/4H62q7t4wAA83nfrJxzuNx3uMl8qEmdJnMPOXzAOFl1M4216secvdv6LJZ+miwIWLUiw1ATpb7SUSBqraRjB4oidwzqQgBSgkSghiJxFOPeKxX35A0NPfuv8tb2rAX7KDfpGnk1+0Hvj+qz0vhIK/Z4YS9UmCDCuYXDR2M/zd3HeIRprwgGScwsUi3b73PM5g7Boh9MFC7CWcr3OTFSn5CyRgjktaHKKPIoBz8OTipoAkVrRpSeRKhQSX0hqmGcqRvz6Lwz/5ND8Gt9hgPkvftRz8y0ZKe7TUraWTJIIgRSkvj2/k4t3a2EvG59nhcYah3UW39NUXJJ+7xbvfbqr8+4rhUDOv3dQTxJm6jVm4oipAGaW5dBrugkGOtDtTZAJsLWYyTeGiN4a46KZhLWJoyrF+S5tHdgm6enYmumajT/24bM/fXyxEcRi1/hhz3tvzirvEYvzYucScJrzlrp4t4UQWOfwhEQh8JA8Z0d5q6WIDhVdw4Jb1HKkECTOYUT6WykExlkiY4gTQ2QMVWcpmohpaak3eUSr2smu7yW/eYCmFZ34TSFSCJwFFcWEs1UytYjy4VGe3HWKVWfmuKocUZUCOW/ohtdZXOIhtETEVZvcfsfZp5+YX7Nw7JCCne4/+m8ZFI7dCJpjZw2g5oFpsfsuMYeAjFOMJhXm4ip7VJHoFsdvfWYdzYMbefCRM+z9i59zXb2HrFK0Op+hepmijShhqGY0rpDBa8vhhz5BTwv+slbya5bh9bXjcgE2MdgowRkLwpHUHOvUOIOdswRfKtJTjilay87IsGJkli1zNapKIhaBZGPTjCeVwrpZJ7j0/WceP+bYIfQDHBA7gMTZb+Sk11yycSKF0DSQe6njLl287wRP+WcZuiymf9MK1hey/NbtfcjeLEQT/P6vX80PWi1P/Og1KnMwfSBma8dyBtavZqCQJ9tRQDfnEIUM+BqUoBYoqkpQqceYJAEpU1CRAislGw+d4aI3h2jtMDBWJ5aCZuCXPc2X2wtsLtUWLnseeBuhqyJrk7zUzSUbf2MHXP8AB1K4eqT/prvzwvvOrIkTKdBuCUDM0xPngEtAFsUuf4Ij183yx//5YlZdciGjh4/xyONvMVVNqFQSnDFcf1EPW1c2MVdL+Op/HKPnVDO/unwDo7FHjCKWjrnAo5jPUMmGlAtZ6vmQJNSkPJz6XuRrLth1iqueOYYXWV6wku6MYNA6LBAJwX9XHtcdGaLZWMzSxS+8doikRXm6aKN7bj/z5L+pn3KDjprUN6WU3QaLS9kkBbvFlhTznzU8w8Ku/gk+/vEVrMr7fO+hV/j2o8fp68qxdUWBgY4s2cDn2dfH2H1khotXt7BxRYGf7Rnm+mN52qcjClMVWmZqtJZrtFQqtJTKhLUqBkM19EnCAAHEvmLg+CQ3PHoI3zgeygU8GnpcHSc0p7BEIgQ/9n1WTRbJGIcT54XsAms7JxAisW7Nf5kb+Jqu9wXXeUJcWLWJW4h7HDg3/+r8KEiBxTjwLR0Fn93HZtl1rMpvfmQty9oCytUYZ2Hj8ibec1E7Dzw7xLefOs0nbh0gk5dEQ4Ks8Eh8sFJikehIULCGXFyivVxhfGqa0fYCEz1dZCLFVU8fIYgNQ6HHj6zia0kNpRVRYvCt5c0woFqNaY8SIiUbDLOInp1L6VlIVbWJ84S8sN4XXCct7k4/ZTk7T2lLNJZrqI9FdOdbyb6uGS76xRW05zM8t3+Su2/qo7VJMzZVpR4ZosRQqiXMVWO239CHMZazc5Ybf/lSHm4bxiUO5wvinCIJJYmviLWHQeNHip7ZmGWjMxTOjnPVTw/TPloGJXjED+mNizx28BhHJ2fxlaSqFF/1Ay4bm0Zbm+6+O8dUzIdBIxSEENaXCou7U91dGPxzAT0mjTbBeTJUzEufhsLxnWTIK1Pa7vi1u7dw8OA4parhig1tVGoJWokFdSelwDlQSuB7mrHZCrd/bAv75Ayl3TMMVvIQJwgpsEpilcAKgXCCsULA2bY8F+2dYNVbM0gpOJsInu3L8wcfW052VTP7To5x8vgkfa156r7Ha1KxeraCBlxDVyBYojg59yRi6zx1d371FxAibABcephbFO/uHAaAQyeC050VVt0Y0mUNIzMR+Saf7hafxNiFPxJCLvyblI1nJVjRYQhkwluvlthabYEkISgb/JpFGQdKMNURYBPL1tcn6Ds+hxESbS3fkD7tF2a5ohPyHVk2XNnHULHC67tPc3c2IN/axGO+x5qZUgqgQix4L4uM0ABFgRAZjaBgnEUsoJ5DSLHA/Qi3IE8X6C9RFGsGpGJ5TxPgiGKHkvJcXtAASyEgSRwdbQGDvZ1ILZmarpGpAlqmNGdAVw2qkhAUFX4poXm0BpEj8TVaw0QCr/e38qcfeQ8RCcnsHKo0w42fzPJ6+36+/dARtvf1Ue7u4ofLu/ilk6NUtDyPyd38pggrwFlb0KR5xKILZ4nVEALhzn0fK8fymQy7XpziQPccN1zWS5LJMfzmGYSQqTsbu4hIBUlUonvTjeR7fUb3PMuTT01T0IO4uQQ8sALwBRoPVU3w52pYT+ICsBq0EnwvFmy5fRt9F1/OTOLQysPFCclcmYsuvxlv89P8+T/+lD+cmuNIaxNPLWvl5tFpSlohGxnmQp5yjt6knHf5JXHfCIYFGhDnXEjhKKI4c3IZrz4/Tblexmvvo33zNky9jEksCJmmSQ5MbY5CzwDZ/m2YyWM88PAId/VdyLrrVvCXQqBji0aihcer02NMiDo2p7AeGB98KTheMby4ehn3fPAKKlGEjOu4egnrIlxLE8XO1Wz+7d+m768+y+8FHr9drjHV08ZbTRkyxmIWZ6duKaPJeX4Xi9DBNRKqxXm7QGCcw7fwTEfALZlWumcVZQNu6C0yHX10X3ojQcZD2DrC1JEioW1wC63bPoKbeRE3N46NfXrzzbz/gmambt3MP6EZMjGf2P8kXxjaz5xLkFKiAd8IhsoJ/yMbcte9t9PR2kxsHEJKGvkZJBHSVClXJ/m1D11J9tZLea4W8/kk5oXVXZSFwBOpeDvf0wUg0wKEOyf4ltDHuYhwAgLgtC9w+Qx3xRIxLdl/sojM+KiTz5NrCum++jZ6rrqVZVfeQs+1d9C65kK82afR5m2OnapDXRFqSdTcxY4/vIfxOy9h9QsPE2c7+JuNN7JM5pgpR7xRMXytHPN3Gwe49y8/zfuv2cxctbYAqEukiZBYAbU45jc+fSv3r+ujs1jj2t48Dy9rIUzMgiOfA/T0oef9fAHo3GLhIBbSX+vAt47dzVmuFRphLe+f6OKfv3eKatUy2J2jdeIZeru7cNlWpNC4iWOMTJxmRiZMzVq++/2z3Nx3OUHoMd23huYgz223bKQ891/Zuvlqvvj86+jpEsIY9LJWBq/cwO/eeik9Lc3MVsvvWPxCYcUZhJSEfpbVfXk6r9/Ctw8N86lqzDNXDLD7kSKXGsecECh3LrkDED/uu8mdlzUhhcQ5u6QMJYEI+MHyNv638OhKDCSOOZXwZGGcudAw3h5zzUc62NabxxrH0ekKjz88Rtspn47I8Va9n+ylvfzGbWuYufYOpI0RQpILMlgMtShirlzDA5oLWTwVUE1iosQihX236h3OOZRUJMbw0IM/5vLLtiEyBb7w2X/gG8PT/PRjW/iXg2e55JE3uTgIqeCwxqZ6H4FezPMLce/sEvR3pLt/KOfT7Xt01R0GkMKRt5pfnBjg0f5hOsZ93vz6NHsunMXzJLWDEVsmckxmEz40NECbp/huRWC2XpXWEowlzAZIPCKbkPWzZP0s5ahCNbGU62WU0g16fXcDWGPJhwV+53N/wJe+9M8057LsPfAcvdds4fl/f4ZrXzzFwU9dzL7ZErVnDnNJJo/SitiYtIyWLtwuxP079H/jvXKOY00BV7j5zLCREwmHVQkHCyWGr4q5uK2V9a9lWP6yz2Wqldlr4Y2OORId0Z9EVJKQYrYNYSJacu2cPH6KT//Kvdzx/o/zCx++h5d//gpZP4c1BqUk50JWvOvuh5mQU8On+Or/+Rc2rl/Lvb/56+x7bR+XXLmeJ/Mh/okZLto3ygWffS+zd27lydoExWqFQGksDp1iiFyoxqa06BZSX0FaYakLQdlXbDEsqfBKB06kv0882HtBCS+SOAenvRiMQ1tBRVh6hEcyNcfkdJE1ff38wz9+hS/9zVfo7OhkZmaGEyfeprOjg67ObnqXd5MkSVo1FCwpxS02gC8Djh45jucHCCF57dU9bLtwPX5lmjO9HVROj7Ltp8c5uqGDdXdfy8SaLp7/p8fZMl5ldWvHORHkWKJd5qERB2jnmPAknq8ZsC4VLoseBsdkLWJiusb4cI0TYyWOjc0xfLrM8EiFsVKVsjP4QtA0VSJ2mgcffJA//P2dfOZXPw1YhIDBwVU8/cQz/O0Xv8z42ASe52HnvXOh0HVeCJCQacrzie3v4wu/+2Fe2bWHo4eOcdnGAWRXnjNakZ2ucf0jh6hWa7ResZ51f3YPb67Jsm96ND2rWFzpma+7L8qjNTCnJN1CELq00igWuFVQcYYT0yXOTNY4OVPjzGyd4WLEmVLESDnhzGyVaRODg1VC8PKrb/Cnf/IX/Nv932Tvnr0MD51lw/p1XHHxRrqXdfPojx7nlZdfW1TZtEsV2ZLwtPieR2tbCx+4vI+Vy3s4dORtepc105l3nInBZT1W7h/jjvv20XxiDK8zz+Y/+SjDA34aAvMSUbj5BGgxJaZgOKclUsoFTJhPNpQT1J1hKoppCiROSLROf5cYi5OCsrPM2QQUJGHI//3y1/ngh25jw8b1PPmTJ7lg28Xcef0KPvqhTfzRl17kvh+MsXfPG7znfdeSyWYwxuBQ5wMAAoiimIG+buoiy5//68s0NTVx4sRJ3PAwmzo0hzzNjUmdKNAMvDHOXcenGO1pIqsUp1sHkPNcP18DTN/L+ZoI1jkkjrKSNC8uaztwUoIUHJicpiYMga9RWvLWiUn2HhzFOEfgKYyEkk3re361ihLw2Xt/jZNHT6C1QilFqRoxMl5mtlQjDANmZ+YYn5hastPn61gBmCSmOZ9l9eAqntp9GqE0SgoQku7QcdLXiAaIm4zGJbDs+CyFI1OsTXzkOcnboMGF2tki8EEQWEdoz/2xBaSWFMs1huoVsnmfuVrMrteHuP1EO79+djlvvDbK2ekKQUYzbusgBKpa544Pf5Delb3093RijEVh+PGLp7j3z55kbLrO9OQEV197DS2FFowx76Z9ztnCGkgsF1+wke72Jg4efIurLtuGyBXY0KwYa/KoIFL/semaTKCxQVpzVPc0D35+gQKFeEdfDwHawaQnKOUzXGcFphGKUkrOTBSp5x3/robQZxL+eG4dnwhWs1W3c0m9wMtjo7yRzHJN0sZFup3jWvF8PMntVwzSs2olSRTx7X97kCAIqNVq7H19L9e/53r+547PEyUlPF8tkeUsTredBeNwUUJzRuFMxM0338Rvfe5eZk6eoLk+yp6S4tCZOa4UqQ/NN2NcQ9ipe/KDn19cPBTnLZ60UEhNwEgh5L0upTgpIE4MZ2aKbOvsZqAe8Dk3yEVeJ1UiIpewPGjmJtPB1HCFe/IryQvFvmyW16JxLl7VSdeyfq696lLWrR3k+MlTBL7HPb/8cf767/6OyYlRDh15kxUrl6d0eL4RnAVrIXbYqEqoauRUFRtbYjwevO+7rOoqcN3KZr45WqN7qsoKwCzpToF4pO+mhTKBm2+EALIBhKYRP0UBz65o42+sxnPp7s9Wa5yammVrXzdYCSZmhIgx6fCAVcLnZ0MjbAibGGjJgzN8IZen+ao8K3MVLvrFT9OzYgCacg0MUkCeUnGc53/2JNuu3Eq+0Iyxi1Sgs+cMkFhcPYaoiKtPEiQV9ryylzf2n+TSwX429XQRmIQ9Z8r87YMn+T+1mLwTIEH4Ho+Nnj7XA7CNGmrGpYKj2qiiho3eXd5apHNMB4qeaoIVKUDqBjNExGglyOLhXB2FRDrBRR3tdIYBJjEoKTmhBHd15biyI+CpB75O77ar6R1cT5gvkGA5OzLEyNBJNm/ZTCGXI05ihFSLdn4BsHDGgY3A1JAmJiqXuXTDIFcNriApVqjPVYlix7Yuj7Vrm3j0wDR3+oIZ63h2apiXxyfR8/093zmcgF3ScLbgM9eSAQfeXMTyYsSmyDHmYNg6ehBY68h5mnqckDiLbsRLAcVqEaT5A5LOTEhsHVopjirJdKvP5hbIhFluXutx9PBLHDu0iyQM0bmQ5q48V2zdTNCcIy5XEZmg4R1iKQpal4aATVIjJHWksdTKNWrVGjKKETikMzhruH1TgX89PM2pmRnOlCt0zNV5T9CBtkAGwbB2PNUeMLe1lw0XDrC6I49MLBOzVUZG5zi+6xTvLRnWYzEChLUEgY8QgtlqjfZ8EyaKsQLyDdVmnWM+hxE4vuoFXDaYYVmTolyP0VqxZdUATgpiJRH5LLKtmbqNiKuziNBLFyrdu1OAs+CSlAmMgYaX4izCOjAGgcFYS29BU/IUy8/W2KwUoWoicg6dsY7DnuDBje1c/r5N3LGslZnOHPW0HspgZ551qzsRg138wkMHyE5USJRAInDWsbqjhUNnJ8l4HtnAxyQGM48qKr1+Twqe9HwOd2f4n5syRLFrdHottTjCWofwA1y5hlMKlEL49YZ7h5yvgd6BB9gGI1icsQhjcMbMoxoGCJRAhx5ohbaGChYJ6LMKHlvVzGe2rOSat8uEz5wmCjWTPXlOrOtgdKAZowSFrM9Ia4ZVoyVQOpUmxlLIhPS3Fjj49hjdnXmWFZrQSqUuqwQYy+R0kf/wQz5xUzeFjKQcOVSDi6SEx/Ydob+jlYs3rKQWxRDHYOpg4tTN35kGppadxwRSYwpn8bXESIFUApzAOYkWkEiLM4LQOZyUCyCvXyp4/LL0uevlYVypjvAkGEf/0SkufH2Ike4Cz9+6lg2vDbN6/yhJk4+wboFGTGLo6W6mdV0bB18+yamJWbK+R+hprHNEzlIu1rhjWcgVq9dTixuLn6dbJ1jX004+DLFxgvAUJAmYBGfixmsJUi207M5pgUbT0zrCIMDWI86cHqfZk9haHVsz4KDJE5wpGiqlCGUMiRSk8ycCLfIhd5YdhgSX9dLF+QLX4kFXwLK3S9z5T7sRCkzWSzW4FMhGViisI+kOUJe0c0HgUT4wQVU6akmCtY7+lhxRU8yplhjhKVxtKadb59jU14W1jihOEMbDJRZMkgKcScCopW4/bwjjcMbiewEnjh3mn//+GzhdYd2qPo4eHSXXJKnXDYFWTBVjQmP4cU9M3KhE40BfLzRaSWLrGgDioNmDdh8cxK0+slpbMLpw8J1A8cG6odk5nJKIiQh9dA53UTNZX5A/VgIlcJ5EKsFQqc6USd41m0NAPU7SClSjFJe6uD3n5taeO9Q6cCng4SzOOjK5LA/c9wTbrtfc9ouX8ui/jXD1jQNcel1LGiGeY+RwlUALKrFlz54i2gdrHXqVkLjFBRcpEBUDU1GaC9RsGstxuuOec+Qd/DjQfKIaUZcSfzZiz5tFkpxm24UFal0B8sAsdrqOHyjwBKVahEss4l2MMO8RFnByXow1Wmu24Q1OLVWBzjSOEFiXtqA2XNjP0187zPF94/ReNsAj3yshlUzxQaWbp5XA99LlKgE6VOIcs8zjTd0ibJK6e83iVHpRErBCcFs94Q/yITfEioHEgic4huD4jOGyDkO9LwPLc4iRCpyqoE7GlOfAJDYt57tzs2ZLyttSgFJplilkej02RfeFY5xLac8ZhLONtlKaL1SLYJ2mOlMmX01YsXWAeqm+0OEyEpJaQhJbrGiAYFk0hL1Z2g5DAknDTIuKRVaAD9xRT/irbMBfF6v4CnoDwUHTcNt6AhkP1hRwGwr4s2289PNObooc7VlJbJfqGjdPWL4CT6UsYzW4xkVYWOBCZ5eGgbDgNNYJjEmZKdeS5aLuAuvXd8NlyyAxDX0voNGOS+WrRb8U17nS1wudk0a7BIxLxcS7pKLOEzRby8EAvlgI+JUk4kioiBIHiUVIAbFBzAlMKMm3+YhAMVY2dOUVsXXvlDWexIU+zvNwKgDhn8v4rMVZs6gcsCgZwoBwKSSY9Lm9KaQQ+rzwyD6CwyPkMj7CgsGiMx5hk4cONC6y6DdmSxxt0ayRIZE1jV5ZwwjuvNwbSDyBThxPaMEl2ZihUPGpUpaiEbwvtlBvKDeVJismgtBKVhYUJ4qGC/oENnbnRtkQ6eKzPgQBzs+ADIHGpljXcMVGe2pBBc4DZYoFypOcOjBFdaZOLuuRCX2efu1taq+e4ZbbL0FLgRd4iIpl/O2Zxh479E2z8PVkhs92dNAjFHGjCYIUkJyrfzsg0RBKGHKCPVnNZb6i31k8G/NGRRFGAkox6FQFIiwiclCXrGvR7B+P+fCGdNFuHoU8jQs9XBDi/CxOZoCgMR3WiHfLO6cUHQ0WsLjEoLXi2JkiLcaQkSkoxgn8wt3bqA9czA9+Zuhs8/E9CcrieQ7fk+gThbNMJpYvjsyys3sNOS2JhEM1CqOu8VpLhZaWY/WIv6pVKKgydZWlnmliYzbDZBSxrARMg/NkGsfCgJK4esLyUPDorKEuFaJJpmivJE574AVYLwQVggjTY2Uj2xPJEsp0i8MAh4kNSgsCLahJiRaaWr0OUuIpRb0W8eiPx/niN6foaFWkBSaxUPnW8XXbWC0sp05O888HT/FbK1YS+n4aYyq9SJvEPDB2lje0ZaglILssIk/CVDRNffQ41ZZOYtvDBqFhLkEokXqBk4jAkVQNyzOKWuI4TchAs0/dgFAKJzxQPk76qQE8v0E3pAY8vyeyqHvlHLQ1aUZOnODQ0VF6exVJ3dDsezgnMC5N8/1MQGdHQHuzWlDW8zUQ3bl8NQ7YstxxWO7nf+0/xE25ZtYXCogEhus1nq6VOL25h94tK7ko46WkgUQqyezRQ7z96mu0yxwbujqInUXMRBAIrBVINIkQFPKanLScmNWs6G8hqcRI5SGEApUaAU+CTBeGSdlJcB47uVTASGFpCjSPPLOPxx4+RHWkDAMaKQTFKKJkHForopoB60iMwxiHsUvZR5s4wjmHEYJ1117A1Lo+frD3BEG5iMxo4o4A1d5Ja8ZQnziFDXPk27twNh0lLvT1U3/tLS410OZ7lDEoCbaYkCDRxuESgSp4dBMxMmPw2jto0jUcUIsdVqaK0QnXmEm3KQZYuXgGPUV74wi9VDH+xb++yOHnD7NjbSfPtYUcNgmtDQ0hpEDKVO8rKc+dYqEABIG16IVWsYO4FtHc1kz7LZcSG5MWSp3l2KsvUK6mcrRWixhsuhovzCAQxNUK8Wydzua0RJ4CloQ2hZPgKgZhgdjSIxMefeJNquUaKwba6WzNsKa3GU866nGMUmoB5Z206fjCPBU2WmOhD6fOFPmH+/ax5uw4X9vSThAGPGbPJYmeg0Bo6rEhim06rUZ6n0Ek0tHbBMHqahktrLNCCDnf73OxIYqSVKhISb1ewySWrt4OlNacPTVMVK0QZLJYHCaOUfWYdp2eWEiBiB3RVRmSCYf3epTiQc3SYiLEyBR7nqvwEopZNDoT8xe/cRt9nTlm5spoTy+dTBE2rVcKh6clk+M1Pvf3L3E3MZ/c2E2lVl/YWuvSYYnYOGIhCAMfYxxKgTVVKs6ju274b6ePoazjSCZntXAUhXAttlHFWBiLbUCu1h7K84jrEUprlJLE9drC+FxcLiOiKNVpjVaZCyTBT2v4vkCotGhDxcJshU7t2NDXhLKCnxw6wPef28vs9Cm+8kefoK+rmem5UpoFyEZFpuFUDov0NF/+7l76D53lbJvk5dCysbOZxLgFsWctaKlIHNSS9N4Wm0QLJv2dU0d579y4q6DFJaWposa6E1qpbbGN07tRFgakUg2gtYf2AqIoIlfIIwRE1cpCUhOXS2hPMhtFjZE3A1ogqg4RA15aP6TuOHFygiCfJzZQrNXYf3aYqy+7hLfHJ3j/vV/g8792Ox+5+SLKtRr1eozy1ILeacr6vLTnKN946PusdtBaXMaDJ0dQoaOgNAdqw6xY20sS15EWnJBENgW+wNOgs3SahLWVElPCd9pJURXuhJTWvaCcAItTjZsZxGKNah1SauI4TtvRQUC1VCSJ6mAd0WyR3r5+jhRLRM4ilUp3TIGVgsRBLhty/OwMJ0dnaO8soJ3g8MgwkZIEQcDy/n5aOnv43N8+xGd2/j9GRyZpDRVJuYKtVjHVCtrE/OCJ3YShT2VVNw/ZM1ysslxcy7GmFNJPhii9U4YEiJwgk/GolwxRTeFslVlbpS4VWWech8A6+4LUVnwfY5EOGVerROUycbVKUqvh4pjTR/ZTmh3D83ysMYS5DEm9zNsHdnNi/88pTk9Cs2ZveZTHDxwlbMqQyYb4gU8mG9BUyHF6eo5/f/FNOtcvJwwDpHW8MXqW1tY2nHNEUUS+KcvWzZt4bt8oH/q9f+G7j+2mRShEJYJKRHFkjn2Hz9LW1sqKti7Usg7+rH6EjOfRHzSBTHsYwkupMEFQiy1KK5R0CJtQ0T5f6x1gUnsyFhYf+X29Lpv92dNHD+ytVqa3GhMbJ4USUoG1xFGEH3r0DPQiZDr75wcB3f3LsMZghIdp70B4ksFNq/jZy6c48dSLXL9uJdl8jslahdG5Mi+dGMZf0UNvfzeekOwbOs1waY51fb2YBtuYBkFvWLuS6dk5fu+rT/H8ayf547tuoiWX4djQFEOTJXoH2ihVq6zoXMZRY/i78WPsCDbio6hYh/Qk9dgSW1BKEVlDU17hhCaMynyvo9sEKPknbx/c+60blv1M37hzZ/LRay74y1u2f/Q7+WW9DpdOjJkkxiYJfphp3ECQZkfOOqRS4Bx+NocO0qaHUBL7gYSjT+zigf1vMbn7FQJC/HyWzq1raWlrZnKmyMNv7ce259iwcT1RZN4x+VWrRzQXmrhg03p++PoJDp65nx/9ya9w8PQ45cSQCUPiJKGeJAx293HQvM0XZ46xTXsom6q/+TlCrTXlUh3jJzhnUNJDJ7FrtVb0UfvLnc89m+j7779fbd++/b5HHnnw3mwud01prpQIKbSUEmstlUqZ2Fo8zyMMM4CgVqtQr9cpz0wjpSSXa0I2EvzNd7yHqcvW8uK3vsm6cCU5P0QJwdDMDF9/czcf+Mj7+dVPf4zDh47xpS9/a8m4i5QSKRUTE5OMjozQ19PD1NwMiZX8/MgQuaY8c3NFRsfGiOM47Rxbx0/KJY74ijvYhCHN4eou3ajEpjPMUnpI7ScFhH6mKfuC4u37tv/SdqW3b9/uhBDuhz/8f5+qVeVupURzkiTGxE7NzE6jtcb3A2rVKvVaNa1FmoQgCPHCkCiqMz09RXNzC57nUZkpoqSHnwmIa1UOTExxsjTNntNvc/kHruEPf+dXmZiaYcWKXi69ZDMvvLiHbDZECEUURQwNDTE7OwPOMT1bZFVbASk8Dp4ZJZfJMlsscvbsMJs3r+c3P/ufwaU1xR985z7myjXavAy1ekIpttQSSxRbCp5GSc+gtFaJm32rue1TAhwPbEIKIez9998v77jjk0er1fp2KVXs+74qzhWTTCZLS0tbY2cklUqFOI5oaWmjqSlPJpOlubmNpqYmisUZbCPTaGouEPsZvrb353zz6F6enz5LVMgyPV7kwKHDAPh+wLXXXkwul8Vax9TUJAcPHmB6egqlFFprHFDIZRkv1zg+OkU29Glv76CtrZ2JiSm6Otu47rorue0D7+W6ay4nTgzS95BKEQuBEYK4bgiVS6RSCkRsiLYXX/7Ho9u3b5ew00qAu+66y+zYsUN/5CN3P1Gt1j/onJvOZEIdBGEyOTlhS6W5dGFNeTzPZ3p6klqtlk6QWkMmk0Nrj0qlTBiGDA+d5aEX9jGjPTauX8+mwXWsHxxkaqbMrl37EI0KzooV/Vx6yWYmJ6cZHh5m2bIeWlvbiOMYa1MFWItj6lohpCCO08ry8uUrmZ2t8KOHn6Q4O0u1FqE9P+1IWTDWUTcOqZQVlkQjtbVuOpD2g6df/8YT3HCDfuCB9BbahdGrnTt3Jjt27NB33vnxx0ulmaviOHkmjms6m83KtrYOcrmcyeWabEtLm/M8n3q9tiRPzWazCxf+la/+KyDZvH59KpaSmCiO8TzFSy/tZWR0HKUkWnlcfdWFdHS0snr1Grq7lzEwsILe3n6a8gU6Wls5cHqM3//H71DI+ExMjOGcRQjH2rWDHD12htGxMbRWBEFAHBtnEdY4TOIaNW6rdVvonpmeGrvqwK5/epwbbtA8e+7W2SWzZzt37kzuv/9+ddddnzl0992fubFSqX4yDMNXqtUK9XpdVasVaa0RnpdWFavVMnEckSQRzkE2m2H//gOcPHmKwTWD1KNoSdnb8zzGJ6bZvfsNIE17B5b3sW3bJoxxDQMaOjo66e9fThCELB9YznAxYi6CJI6Ymyum6bBUjI5O89LLr+OsQUqFcGK+5aNq9QTP16/se2vkk7d97s9uZPJHh35p+3a1ePHvMMCicJDOOfGf/tO93/rQhz56hXP2ZmPs38dxvLter00liTEpdxustZTLJaw1KKV44403GyGh32Ww0aKU4ue79jEyOoaUAt/zueLyCwjDAGMs1qaGSJIEY0wD6ROMSTDGMjMzg7WOJLFIKXn2uVcZGR1FamWSxEwhxO6qsX8/V4pu3vmNl6745q5j34oSK2CHnHf7xY//D31Fyxiv7qM6AAAAAElFTkSuQmCC";
(function(){
  const l = document.createElement('link'); l.rel = 'icon'; l.type = 'image/png'; l.href = APP_ICON;
  document.head.appendChild(l);
  const brand = document.querySelector('.brand');
  if(brand){ const g = document.createElement('img'); g.id = 'app-logo'; g.alt = ''; g.src = APP_ICON;
    brand.insertBefore(g, brand.firstChild); }
  const aboutLogo = document.getElementById('about-logo');
  if(aboutLogo) aboutLogo.src = APP_ICON;
})();

/* ---------- 中英双语（只翻界面文案；后端日志与技术输出保持原文） ----------
   设计：字典直接以中文原文为 key（源文案即 key），缺项自动回退中文，不会出现空白；
   译文由 DOM 文本节点/属性替换完成，并用 MutationObserver 覆盖动态渲染的内容，
   因此新增界面文案只要补进 I18N_EN 即可（tools/i18n_inventory.py 可查漏）。 */
const I18N_EN = {
  "0 行": "0 lines",
  "1) 本机代理（Clash / V2Ray 等，常见 127.0.0.1:7890）没有绕过 localhost：\n   在代理的「绕过 / 例外 / Bypass」里加入 127.0.0.1;localhost;::1\n2) 或换一个不走系统代理的浏览器打开本控制台\n\n验证：到「AI 助手」页点「检测请求传输」，显示\"发送 N / 收到 N\"即已修好。": "1) A local proxy (Clash / V2Ray, often 127.0.0.1:7890) is not bypassing localhost:\n   add 127.0.0.1;localhost;::1 to its bypass / exception list\n2) Or open this console in a browser that does not use the system proxy\n\nVerify: open the AI panel and click \"Test request transport\" — \"sent N / received N\" means fixed.",
  "=仅供参考、": "= for reference only,",
  "=多数时候成立、": "= holds most of the time,",
  "AI": "AI",
  "AI / 桌宠": "AI / pet",
  "AI 助手": "AI Assistant",
  "AI 助手 · 点我对话 / 右键更多": "AI assistant · click to chat, right-click for more",
  "AI 助手（右下角悬浮球）": "AI assistant (floating ball, bottom-right)",
  "AI 解读": "AI Analysis",
  "AI 记忆与摘要": "AI memory & summarization",
  "AI 请求执行": "AI request execution",
  "AI 请求执行：": "AI wants to run: ",
  "C:\\...\\id_rsa（可选）": "C:\\...\\id_rsa (optional)",
  "EnvKit 已退出": "EnvKit has exited",
  "EnvKit 已重启，本页令牌已过期，点击确定自动刷新页面。": "EnvKit restarted and this page's token expired. Click OK to refresh.",
  "EnvKit 环境助手": "EnvKit Assistant",
  "GOPROXY（Go 代理）": "GOPROXY (Go mirror)",
  "HTTP 代理": "HTTP proxy",
  "HTTPS 代理": "HTTPS proxy",
  "HTTPS 证书异常：检查地址与系统时间": "HTTPS certificate problem: check the URL and your system clock",
  "Key 被拒（HTTP {0}）：检查 API Key 是否正确": "Key rejected (HTTP {0}): check that your API key is correct",
  "MySQL root 密码": "MySQL root password",
  "MySQL 服务名": "MySQL service name",
  "MySQL 端口": "MySQL port",
  "MySQL 连接": "MySQL connection",
  "SELECT * FROM 表名 LIMIT 10（Ctrl+Enter 运行，仅只读）": "SELECT * FROM table LIMIT 10 (Ctrl+Enter to run, read-only)",
  "SQL 文件": "SQL file",
  "SQL 查询": "SQL query",
  "SQL 生效": "SQL apply",
  "SSH 连通": "SSH reachability",
  "Token 消耗": "Token usage",
  "W 目录": "W dir",
  "W 端口": "W port",
  "W 脚本": "W script",
  "WeBASE-Front 地址": "WeBASE-Front URL",
  "docs/eval 下最近 60 份": "Latest 60 files under docs/eval",
  "docs/eval 下还没有 .json 产物": "No .json artifacts under docs/eval yet",
  "http://127.0.0.1:7890（留空 = 直连）": "http://127.0.0.1:7890 (empty = direct)",
  "https://api.deepseek.com 或完整 .../v1/chat/completions（都支持）": "https://api.deepseek.com or a full .../v1/chat/completions (both work)",
  "max_tokens 字段": "max_tokens field",
  "npm registry（Node 代理）": "npm registry (Node mirror)",
  "npm run 脚本名（可输入自定义）": "npm run script name (custom allowed)",
  "npm 源": "npm registry",
  "p90 {0} ms": "p90 {0} ms",
  "package.json 推断": "detected from package.json",
  "sk-...（留空保留已存值）": "sk-... (leave blank to keep the saved key)",
  "temperature 固定为 1": "temperature fixed to 1",
  "trace-*.jsonl（比审计大得多）": "trace-*.jsonl (far larger than audit)",
  "usage 字段是 v2.7 才加的，此前的对话没有记录": "The usage field was added in v2.7; earlier conversations have none",
  "v2.7 起才记录": "Recorded since v2.7",
  "{0} · {1} · {2} 秒 · {3} 行": "{0} · {1} · {2}s · {3} lines",
  "{0} · 已 {1} 秒{2}": "{0} · {1}s elapsed{2}",
  "{0} 个存活": "{0} alive",
  "{0} 个节点": "{0} steps",
  "{0} 个被占用": "{0} in use",
  "{0} 个轨迹有记录": "{0} traces recorded",
  "{0} 升级中，进度见下方安装日志，完成后点「检查更新」刷新。": "{0} is upgrading — watch the install log below, then click “Check for updates” to refresh.",
  "{0} 天有记录": "{0} days with records",
  "{0} 失败了，点红标看日志": "{0} failed — click the red badge for the log",
  "{0} 已启动": "{0} started",
  "{0} 条": "{0} entries",
  "{0} 步": "{0} steps",
  "{0} 秒": "{0}s",
  "{0} 行": "{0} rows",
  "{0} 轮 / {1} 份报告": "{0} runs / {1} reports",
  "{0} 项通过": "{0} checks passed",
  "{0}（与脚本一致）": "{0} (matches the scripts)",
  "{0}，{1}！环境很健康": "{0}, {1}! Environment looks healthy",
  "{0}，环境很健康": "{0}, environment looks healthy",
  "‹ 上一页": "‹ Prev",
  "① 环境安装": "1. Install",
  "② 程序配置": "2. Configure",
  "③ 链端校验": "3. Chain check",
  "④ 程序启动": "4. Launch",
  "⚠ 请求内容被中间层丢弃": "⚠ Request payload dropped in transit",
  "「拉取模型列表」会调用服务商的 /models 接口获取在售模型；拉取失败时仍可用预置列表或自定义模型名。Key 经 DPAPI 加密存储，仅本机可解密。": "\"Fetch model list\" calls the provider's /models endpoint to list available models; if it fails you can still use the built-in list or type a model name. The API key is DPAPI-encrypted and only decryptable on this machine.",
  "【附加文件引用】": "[Attached file references]",
  "一个小时内不打扰你，悬停找我": "I'll stay out of your way for an hour — hover to find me",
  "一键启动": "One-click launch",
  "一键安装": "Install now",
  "一键检测": "Quick check",
  "七月中出ds正式版！": "The DS official version drops in mid-July!",
  "三色团子！软乎乎～": "Tricolor dango! So fluffy~",
  "上一级": "Up",
  "上传头像": "Upload avatar",
  "上次评测": "Last run",
  "上海": "Shanghai",
  "上游对参数的接受度不一致（只认 temperature=1、要 max_completion_tokens、不支持工具调用）；这些是踩坑后自动记下来的，不是配置项。": "Upstreams disagree about parameters (only temperature=1, needs max_completion_tokens, no tool calls); these are recorded automatically after a failure and are not settings.",
  "上级目录": "Parent",
  "上限 {0} 轮": "max {0} turns",
  "上限放宽到 {0} 轮": "Turn cap raised to {0}",
  "下一步：程序启动": "Next: Launch",
  "下一步：程序配置": "Next: Configure",
  "下一步：链端校验": "Next: Chain check",
  "下一页 ›": "Next ›",
  "下午好": "Good afternoon",
  "下载": "Download",
  "下载代理 HTTPS（可选）": "Download proxy HTTPS (optional)",
  "下载代理 HTTP（可选）": "Download proxy HTTP (optional)",
  "不主动提示。\n          标「已失效」的项目/环境变了，该条不会注入——": "not surfaced proactively.\n          Items marked \"expired\" changed project or environment and are no longer injected —",
  "不发送 temperature（上游不接受）": "temperature omitted (rejected upstream)",
  "不发送温度": "Temperature is not sent",
  "不支持（已自动降级）": "Unsupported (auto-degraded)",
  "不通": "Unreachable",
  "与 EnvKit 的连接已断开（可能已重启），点击确定刷新页面。": "Connection to EnvKit was lost (it may have restarted). Click OK to refresh.",
  "与「程序配置」「链端校验」共用同一份": "Shares the same ",
  "个人中心": "Profile",
  "中": "Med",
  "中午好": "Good noon",
  "主体": "Actor",
  "主机": "Host",
  "五梁威力，变身！": "Five-Liang power, transform!",
  "交易数量": "Transactions",
  "产品名": "Product name",
  "亮色": "Light",
  "人工确认": "human confirm",
  "仅 DeepSeek 支持": "DeepSeek only",
  "仅执行 SQL": "Apply SQL only",
  "仅相关时": "When relevant",
  "仅覆盖最近 {0} 天（轨迹保留期）": "Covers last {0} days (trace retention)",
  "今日统计": "Today's usage",
  "今日（本机累计）": "Today (local)",
  "仍有 {0} 个组件未就绪": "{0} component(s) still not ready",
  "从操作记录统计，不是你在下的命令": "computed from activity, not an order from you",
  "仓库根": "Repo root",
  "以管理员身份重启": "Restart as admin",
  "任何项目都生效": "applies to all projects",
  "任务成功率 / 首次成功率 / 需重试": "Task success / first-try success / retries",
  "任务数": "Tasks",
  "会话统计只在本次页面打开期间累积；今日统计写在本机 localStorage。": "Session totals accumulate while this page stays open; today's totals are kept in localStorage.",
  "传输": "Transport",
  "传输正常（普通 {0}；带对话头 {1}，头长 {2} 字节）——问题不在体积或请求头上": "Transport OK (plain {0}; with chat header {1}, header {2} bytes) — size or headers are not the cause",
  "传输正常：发送 {0} 字节，收到 {1} 字节": "Transport OK: sent {0} bytes, received {1} bytes",
  "传输被截断：发送 {0} 字节，服务端只收到 {1} 字节": "Transport truncated: sent {0} bytes, server received only {1}",
  "位置：": "Location: ",
  "低": "Low",
  "余额 {0} {1}": "Balance {0} {1}",
  "余额 ¥{0}": "Balance ¥{0}",
  "余额查询失败": "Balance query failed",
  "作者 / 项目地址：": "Author / project:",
  "作者保留在不另行通知的情况下修改、暂停或停止提供本软件及其相关服务的权利。": "The author reserves the right to modify, suspend, or discontinue this software and its related services without prior notice.",
  "你好！我可以查看日志与状态、执行备份/检测/启停等操作（写操作需你确认）。试试问我\"后端为什么起不来\"或\"帮我备份数据库\"。": "Hi! I can read logs and status, and run backups / checks / service start-stop (write actions need your confirmation). Try asking \"why won't the backend start?\" or \"back up the database for me\".",
  "你已拒绝：{0}": "You declined: {0}",
  "你已确认：{0}": "You confirmed: {0}",
  "你当场的明确要求 > 我的记忆 > 自动经验": "your explicit instruction now > my memories > auto lessons",
  "你手动修好的": "Fixed by you",
  "你指定的": "set by you",
  "你这吃白饭的用户！": "You freeloading user!",
  "使用 max_completion_tokens": "using max_completion_tokens",
  "使用上方 MySQL 连接配置；纯只读，不提供任何写操作": "Uses the MySQL connection above; strictly read-only, no write operations",
  "使用本软件所产生的任何后果（含环境配置失败、数据丢失、服务中断、端口 / 进程异常、链端操作失误、误操作等）均由使用者自行承担，作者不承担任何直接或间接的责任。": "Any consequence of using this software (failed environment setup, data loss, service interruption, port/process issues, chain operation mistakes, misoperation, etc.) is borne solely by the user; the author accepts no direct or indirect liability.",
  "依据：{0}": "evidence: {0}",
  "依赖": "Dependencies",
  "依赖 就绪": "Deps ready",
  "依赖 待办": "Deps pending",
  "依赖 未执行": "Deps not run",
  "依赖 未检测": "Deps not checked",
  "保存中…": "Saving…",
  "保存后会同步到「程序启动」的目录显示。": "Saved folders also show up on the Launch page.",
  "保存基础设置": "Save basics",
  "保存失败": "Save failed",
  "保存失败：": "Save failed: ",
  "保存失败：HTTP ": "Save failed: HTTP ",
  "保存失败：HTTP {0}": "Save failed: HTTP {0}",
  "保存失败：{0}": "Save failed: {0}",
  "保存开发者配置": "Save developer settings",
  "停止": "Stop",
  "停止全部": "Stop all",
  "停止失败：": "Stop failed: ",
  "停止服务": "stop services",
  "停止生成": "Stop generating",
  "允许写操作": "Allow writes",
  "允许查询天气": "Allow weather lookups",
  "先备份，再生效…": "Backup first, then apply…",
  "先收起来，需要时悬停右下角找我": "Tucked away — hover the corner when you need me",
  "免责声明": "Disclaimer",
  "全局": "Global",
  "全局适用": "Applies globally",
  "全屏": "Full screen",
  "全部": "All",
  "全部启动": "Start all",
  "全部就绪，请重开终端使环境变量生效": "All ready — reopen your terminal so the environment variables take effect",
  "全部日期": "All dates",
  "全部空闲": "All free",
  "共 {0} 次 · 最近 {1}": "{0} time(s) total · latest {1}",
  "共 {0} 行 · 第 {1}/{2} 页": "{0} rows · page {1}/{2}",
  "关于 / 免责声明": "About / Disclaimer",
  "关于 EnvKit": "About EnvKit",
  "关闭": "Off",
  "关闭后 AI 对话与桌面助手均不可用": "When off, AI chat and the desktop assistant are both unavailable",
  "其它": "Other",
  "内容 | 标签": "text | tags",
  "内嵌终端（页面内直接操作远程 shell）": "Built-in terminal (operate a remote shell in the page)",
  "内置默认（12）": "Built-in default (12)",
  "内置默认（300 秒）": "Built-in default (300s)",
  "内置默认（3）": "Built-in default (3)",
  "内置默认（600 秒）": "Built-in default (600s)",
  "再晃要吐了🫠": "Keep shaking and I'll be sick🫠",
  "写一条长期规矩，例如：备份前先停掉后端服务": "Write a standing rule, e.g. stop the backend before backing up",
  "冻结只能走命令行 --set-baseline，界面只读": "Freezing is command-line only (--set-baseline); the UI is read-only",
  "冻结时间": "Frozen at",
  "出去玩了，发布新模型什么的以后再说": "Out to play — the new model can wait",
  "出错了…点我看日志": "Something failed… click me for the log",
  "分析最近错误": "Analyze recent errors",
  "刚停止，等 {0} 秒让端口释放后自动启动…": "Just stopped — waiting {0}s for ports to free up, will auto-start…",
  "初始化数据库": "Initialize database",
  "删除": "Delete",
  "判分器自检": "Judge self-test",
  "刷新": "Refresh",
  "刷新余额": "Refresh balance",
  "前端": "Frontend",
  "前端 / 后端": "Frontend / backend",
  "前端 npm install": "Frontend npm install",
  "前端 {0}": "Frontend {0}",
  "前端 {0} · 后端 {1}": "Frontend {0} · Backend {1}",
  "前端 · 后端": "Frontend · Backend",
  "前端 未执行": "Frontend not run",
  "前端依赖安装": "Frontend deps install",
  "前端启动脚本": "Frontend start script",
  "前端日志": "Frontend log",
  "前端服务": "Frontend service",
  "前端目录": "Frontend directory",
  "前端：{0}": "Frontend: {0}",
  "副标题": "Subtitle",
  "加入": "Add",
  "加入 Defender 白名单": "add Defender exclusion",
  "加入时间": "Added",
  "加入杀软白名单": "Add to AV whitelist",
  "加载中…": "Loading…",
  "动作": "Action",
  "助手已回来啦": "The assistant is back!",
  "助手已彻底隐藏，可随时在这里恢复": "Assistant fully hidden — restore it here anytime",
  "助手正在忙，稍后再试": "Assistant is busy — try again shortly",
  "勾选后彻底隐藏右下角桌面助手（对话面板仍可打开）；取消勾选即恢复": "Check to completely hide the desktop assistant at the bottom-right (the chat panel stays available); uncheck to bring it back",
  "区块链 :": "Blockchain :",
  "区块链上链数据具有不可篡改性，但仅供参考，不构成任何投资、法律、财务或商业建议；据此操作产生的风险由使用者自行承担。": "On-chain data is immutable but for reference only; it constitutes no investment, legal, financial, or business advice. You bear the risk of acting on it.",
  "区块高度": "Block height",
  "区间内没有可统计的数据": "Nothing to chart in this range",
  "区间内没有带复验结论的记录": "No verification records in this range",
  "升级": "Upgrade",
  "升级组件": "Upgrade components",
  "单位：天；到期自动清理": "In days; expired files are cleaned up automatically",
  "单目标重试上限": "Retries per target",
  "单轮超时": "Turn timeout",
  "占比 {0}%": "{0}%",
  "占用": "In use",
  "历史产物": "Past artifacts",
  "历史估算 token 超过该值才压缩": "Compress only once estimated history tokens exceed this",
  "历史回合的轨迹（只读）": "Trace of an earlier turn (read-only)",
  "压力一只蓝色大肥鱼？": "Bullying a big blue fish?",
  "原地待着": "Stay put",
  "原始大小": "Normal size",
  "去别的地方玩！不要耽误 AGI 训练！": "Go play elsewhere! Don't hold up AGI training!",
  "去程序配置修改": "Edit in Configure",
  "去链端校验修改": "Edit in Chain Check",
  "反复失败": "Repeated failure",
  "发财啦！明天开始吃高级鱼粮！": "Rich! From tomorrow on, premium fish food!",
  "发送": "Send",
  "发送 {0} / 收到 {1}": "sent {0} / received {1}",
  "取消": "Cancel",
  "变大一点": "Bigger",
  "变大啦，看得清吗": "Bigger now — easier to see?",
  "变小一点": "Smaller",
  "只存在本机": "Stored on this device only",
  "只影响 EnvKit 自己发起的下载与依赖安装，不改进程级环境变量。": "Only affects downloads and dependency installs started by EnvKit; process-level env vars are untouched.",
  "只收 .sql 文件哦，「{0}」先还给你": "I only take .sql files — \"{0}\" is returned to you",
  "只看异常": "Issues only",
  "只说明取数来源，不在这里编数字": "Shows where each number comes from; nothing is invented here",
  "只读": "Read-only",
  "可用": "Available",
  "可达": "Reachable",
  "可达 · {0}ms": "Reachable · {0}ms",
  "可选": "Optional",
  "右键可以让我藏起来": "Right-click lets me hide",
  "合法候选：": "Valid candidates:",
  "同时停止由本程序启动的前端 / 后端服务": "Also stop the frontend / backend services started by EnvKit",
  "同步链地址到 config.toml": "Sync chain address into config.toml",
  "后台守护": "Background guard",
  "后台守护：定时检测并自动拉起（挂机用）": "Background guardian: periodically checks and restarts automatically (for unattended runs)",
  "后端": "Backend",
  "后端 go mod tidy": "Backend go mod tidy",
  "后端 {0}": "Backend {0}",
  "后端依赖整理": "Backend deps tidy",
  "后端入口": "Backend entry",
  "后端日志": "Backend log",
  "后端服务": "Backend service",
  "后端目录": "Backend directory",
  "后端：{0}": "Backend: {0}",
  "含密码，所以不在这里再放一份输入框": "Contains a password, so it is not duplicated as an input here",
  "听我说一句": "Say something",
  "启动": "Start",
  "启动 WeBASE-Front": "Start WeBASE-Front",
  "启动中": "Starting",
  "启动前端": "Start frontend",
  "启动区块链节点": "Start blockchain node",
  "启动后端": "Start backend",
  "启动失败：": "Failed to start: ",
  "启动方式": "Launch method",
  "启动服务": "start services",
  "启动脚本": "Start script",
  "启用": "Enable",
  "呜呜我再也不不敢了QAQ": "I won't dare do it again, QAQ",
  "呼…活过来了": "Whew… I'm alive again",
  "命令行执行 python tools/eval_gate.py --set-baseline 冻结；界面不提供，避免误把一次跑坏的结论固化成门禁。": "Freeze it on the command line with python tools/eval_gate.py --set-baseline; the UI deliberately omits that so a bad run cannot be baked into the gate.",
  "咔嚓咔嚓……谢谢投喂！": "Crunch crunch... thanks for the feed!",
  "哇——轻点轻点！": "Whoa—easy, easy!",
  "哈哈哈哈哈，我直接笑出声": "Hahaha, I laughed out loud",
  "哗啦——！鲸式喷水，冷静一下！": "Splash! Whale-style spout — cool down!",
  "唔…醒了": "Mm… awake now",
  "唔，鲜！": "Mmm, so fresh!",
  "嗝～又圆了一圈……": "Burp~ rounder already...",
  "嗯？一直点我干嘛👀": "Hmm? Why keep poking me👀",
  "嘎嘣脆，好吃！": "Crunchy and tasty!",
  "四档评测": "Four evaluation tiers",
  "回到本回合": "Back to this turn",
  "团子": "Dango",
  "固定发 1（该模型只接受 1）": "Always sends 1 (this model accepts only 1)",
  "在呢~": "Here~",
  "城市，如 上海 / Shanghai": "City, e.g. Shanghai",
  "域名无法解析：检查 Base URL 拼写与网络": "Domain cannot be resolved: check the Base URL spelling and your network",
  "基础": "Basics",
  "基础 · 开发者 · 评测": "Basics · Developer · Evaluation",
  "基础设置已保存": "Basics saved",
  "基线": "Baseline",
  "备份数据库": "back up database",
  "复位断路器": "Reset breaker",
  "复制": "Copy",
  "复验": "Verify",
  "复验反复失败": "Verification keeps failing",
  "复验未通过": "Verification failed",
  "复验环境状态": "Verify environment state",
  "复验通过率": "Verification pass rate",
  "复验：{0}": "verify: {0}",
  "夜深了，注意休息": "It's late — get some rest",
  "大肥鱼坐得住": "A chubby fish knows how to sit tight",
  "天气已关闭": "Weather disabled",
  "天气已开启（{0}）": "Weather enabled ({0})",
  "天气数据不完整": "Incomplete weather data",
  "天气数据格式异常": "Unexpected weather data format",
  "天气服务返回非 200": "Weather service returned an error status",
  "天气查询没开，去个人中心打开": "Weather is off — turn it on in your profile",
  "天气没查到：": "Couldn't get the weather: ",
  "天气没查到：连不上服务": "Couldn't get the weather: service unreachable",
  "失效不删除": "Expiry does not delete",
  "失败": "Failed",
  "失败：": "Failed: ",
  "好吃！": "Yum!",
  "好舒服~": "So comfy~",
  "如 dev / serve / start（留空自动推断）": "e.g. dev / serve / start (blank = auto)",
  "如 main.go 或 cmd/server/main.go（留空自动推断）": "e.g. main.go or cmd/server/main.go (blank = auto)",
  "如：ls -l /opt/fisco": "e.g. ls -l /opt/fisco",
  "字段多且互相约束，集中在一处改更安全": "Many interdependent fields; editing them in one place is safer",
  "守护": "Guard",
  "守护运行状态": "Guard runtime status",
  "守护间隔": "Guardian interval",
  "安全与可靠": "Security & reliability",
  "安装": "Install",
  "安装与分发": "Install & distribution",
  "安装中": "Installing…",
  "安装目录": "Install directory",
  "完成时间": "Finished at",
  "宕机 / 恢复时弹 Windows 桌面通知": "Windows toast on outage / recovery",
  "审计": "Audit",
  "审计只追加、不修改、不回滚，按天分文件保留 30 天（exe 同级": "The audit log is append-only — never modified or rolled back — kept for 30 days in per-day files (next to the exe: ",
  "审计只追加、不修改、不回滚，按天分文件保留 30 天（exe 同级 audit-YYYYMMDD.jsonl）；密码与 API Key 在入库前已脱敏。": "Audit entries are append-only and never modified or rolled back; they are kept per day for 30 days (audit-YYYYMMDD.jsonl next to the exe). Passwords and API keys are redacted before being written.",
  "审计趋势": "Audit trends",
  "客户端版本": "Client version",
  "密码": "Password",
  "对比「已安装 / 配置目标 / 官方最新」，可一键升级到最新版": "Compares installed / configured / latest upstream, and upgrades in one click",
  "对话": "Chats",
  "对话 {0} 次 · ↑{1} ↓{2}": "{0} chats · ↑{1} ↓{2}",
  "对话已清空。": "Conversation cleared.",
  "对话式助手": "Chat assistant",
  "对话式能力": "Chat capabilities",
  "导入失败：{0}": "Import failed: {0}",
  "导入成功，已重新加载": "Imported and reloaded",
  "导入配置": "Import config",
  "导出": "Export",
  "导出 CSV": "Export CSV",
  "导出分发包": "Export Distributable",
  "导出失败：": "Export failed: ",
  "导出配置": "Export config",
  "导出（脱敏）": "Export (sanitized)",
  "将「{0}」生效到库 {1}？执行前会自动备份，高危语句需二次确认。": "Apply \"{0}\" to database {1}? A backup is taken first; risky statements need a second confirmation.",
  "将创建数据库 {0}（未选 SQL，仅建库）。": "Will create database {0} (no SQL selected, database only).",
  "将创建数据库 {0}，并执行 SQL：\n{1}\n\n注意：若库中已有同名表，可能被覆盖/追加。": "Will create database {0} and apply SQL:\n{1}\n\nNote: existing tables with the same name may be overwritten or appended to.",
  "将对数据库 {0} 执行：\n{1}\n\n注意：可能覆盖/追加已有数据。": "Will apply to database {0}:\n{1}\n\nNote: existing data may be overwritten or appended.",
  "将对数据库执行拖入的 SQL 文件，可能覆盖已有数据。": "This will run the dropped SQL file against the database and may overwrite existing data.",
  "将强制结束进程 {0}（PID {1}，占用端口 {2}），未保存的数据可能丢失。请确认它不是系统关键进程。": "About to force-kill {0} (PID {1}, holding port {2}). Unsaved data may be lost. Make sure it is not a critical system process.",
  "将执行：\n· 停止 EnvKit 管理的前端 / 后端服务\n· 结束项目目录下遗留的 node / webpack / go run / main.exe 进程\n\n按项目目录精准匹配，不影响其它软件（如 Adobe、WorkBuddy）的 node 进程。\n完成后可重新启动服务。": "Will do:\n· Stop the frontend / backend services managed by EnvKit\n· Kill leftover node / webpack / go run / main.exe processes under the project directories\n\nMatched strictly by project directory, so node processes of other software (e.g. Adobe, WorkBuddy) are untouched.\nYou can start the services again afterwards.",
  "将把 {0} 从 {1} 升级到 {2}：\n下载新版（数百 MB）→ 解压新目录 → 自动更新 PATH 与配置。\n旧版本目录会保留，可随时回滚。\n\n继续？": "This upgrades {0} from {1} to {2}:\ndownload the new build (hundreds of MB) → unpack into a new folder → update PATH and config automatically.\nThe old version's folder is kept, so you can roll back anytime.\n\nContinue?",
  "将生成新备份并恢复到临时库（如 {0}__drill_xxx）验证可恢复性，全程不改动业务库数据，结束后临时库自动删除。": "A new backup will be created and restored into a temporary database (e.g. {0}__drill_xxx) to verify recoverability. Business data is never touched, and the temp database is dropped afterwards.",
  "将用备份文件覆盖数据库 {0}：\n{1}\n\n当前库中的同表数据将被替换，且无法撤销！": "This will overwrite database {0} with the backup file:\n{1}\n\nExisting tables will be replaced and this cannot be undone!",
  "将通过 SSH 在 WeBASE 目录执行启动脚本：\n{0} && {1}": "Start script to run over SSH in the WeBASE directory:\n{0} && {1}",
  "将通过 SSH 在链端目录执行启动脚本：\n{0} && {1}\n\n可能耗时较久。": "Start script to run over SSH in the chain directory:\n{0} && {1}\n\nThis may take a while.",
  "将通过 UAC 申请管理员权限，把 EnvKit 所在目录和 EnvKit.exe\n加入 Windows Defender 排除项，防止 go / npm / mysqldump 子进程被误拦。\n\n弹出 UAC 窗口时请点「是」。": "This requests administrator rights via UAC and adds the EnvKit folder and EnvKit.exe\nto the Windows Defender exclusions, so go / npm / mysqldump child processes are not blocked by mistake.\n\nClick “Yes” when the UAC prompt appears.",
  "将重新走一遍安装流程：重新下载并解压所有组件（已装的会跳过）。\n可能耗时较久。": "This runs the install flow again: download and unpack every component (already-installed ones are skipped).\nIt may take a while.",
  "小一点，不吵你": "Smaller, won't bother you",
  "小鱼": "Little fish",
  "小鱼干": "Dried fish",
  "小鱼干！我的最爱！": "Small dried fish — my favorite!",
  "尚未冻结基线": "No baseline frozen yet",
  "尚未检测": "Not checked yet",
  "尾部原样保留的估算上限": "Estimated cap on the verbatim tail kept after summarizing",
  "展开": "Expand",
  "展开明细（{0} 行）": "Show details ({0} lines)",
  "工具调用": "Tool calls",
  "已交给后端执行，完成前我来盯着": "Handed to the backend — I'll watch until it's done",
  "已保存": "Saved",
  "已保存（{0}）": "Saved ({0})",
  "已保存（{0}），留空保留": "Saved ({0}), leave empty to keep",
  "已保存（配置）": "Saved",
  "已停止生成": "Generation stopped",
  "已停止自动重启（连续失败达上限）": "Auto-restart halted (consecutive failures hit the limit)",
  "已停止自动重启（连续失败达上限）· {0} 秒后放行一次探针": "Auto-restart halted (failures hit the limit) · one probe restart allowed in {0}s",
  "已加白名单": "Whitelisted",
  "已取消，文件未生效": "Cancelled — the file was not applied",
  "已否决": "Dismissed",
  "已启动": "Started",
  "已启用": "Enabled",
  "已填入 {0}，记得保存配置": "Filled in {0} — remember to save the config",
  "已复制": "Copied",
  "已复验通过": "Verified OK",
  "已失效": "Stale",
  "已安装": "Installed",
  "已导出 {0}": "Exported {0}",
  "已导出本轮": "Turn exported",
  "已就绪": "Ready",
  "已开启": "Enabled",
  "已开启批量授权：{0} · 本任务内最多 {1} 次": "Batch grant opened: {0} · up to {1} more in this task",
  "已开启（{0} 秒/次）": "On (every {0}s)",
  "已开始：{0}": "Started: {0}",
  "已归位 ✨": "Back home ✨",
  "已得出结论": "Reached a conclusion",
  "已恢复上次的对话（{0} 条）": "Restored previous conversation ({0} messages)",
  "已打开": "Open",
  "已执行·未确认": "Executed, not confirmed",
  "已拉取 {0} 个模型，已在「模型」下拉框中回填": "Fetched {0} models, filled into the Model dropdown",
  "已拒绝": "Declined",
  "已拒绝，让助手换个做法…": "Declined — asking the assistant for another way…",
  "已按安全规则拒绝：{0}": "Rejected by safety rules: {0}",
  "已按本轮时间窗过滤": "Filtered to this turn's time window",
  "已授权：本任务内{0}最多再执行 {1} 次，不再逐次询问": "Authorized: up to {1} more {0} action(s) in this task without asking again",
  "已收藏脚本 {0}": "Saved script {0}",
  "已整理": "Tidied",
  "已是最新": "Up to date",
  "已有一条 AI 对话在进行中（另一个标签页？），请等它结束或点「停止」": "Another AI conversation is already running (another tab?) — wait for it or press Stop",
  "已有一条 AI 对话在进行中，请等它结束或点「停止」": "Another AI conversation is already running — wait for it or press Stop",
  "已清空为默认（点「保存」生效）": "Cleared to defaults (takes effect on Save)",
  "已用批量授权执行：{0}（剩余 {1} 次）": "Executed under batch grant: {0} ({1} left)",
  "已确认，继续执行…": "Confirmed — continuing…",
  "已经加过了": "Already attached",
  "已结束 PID {0}，可重新扫描确认": "Killed PID {0}; rescan to confirm",
  "已自动保存 ✓": "Auto-saved ✓",
  "已自动保存，已拉取 {0} 个模型并回填": "Auto-saved; fetched {0} models and filled the dropdown",
  "已自动适配": "Auto-adapted",
  "已被安全策略拒绝": "Denied by policy",
  "已装组件会自动跳过。": "Already-installed components are skipped.",
  "已记为全局适用（换项目也生效）": "Saved as global (applies to any project)",
  "已设置（DPAPI 加密存储）": "Set (encrypted with DPAPI)",
  "已设鼠标穿透，点不到我了；刷新页面或从托盘打开控制台即可恢复": "Click-through is on, so you can't click me now. Refresh the page or open the console from the tray to restore.",
  "已请求停止：{0}": "Stop requested: {0}",
  "已读入：{0}": "Loaded: {0}",
  "已读取最新配置（{0}）": "Latest config loaded ({0})",
  "已读取（{0}）": "Loaded ({0})",
  "已进行 {0} 分 {1} 秒": "{0}m {1}s elapsed",
  "已退出": "Exited",
  "已配密钥但未启用": "Key set but disabled",
  "已配置": "configured",
  "已配置（{0}）": "Configured ({0})",
  "已采用": "In use",
  "已重置（{0}）": "Reset ({0})",
  "已附加引用：{0}": "Attached: {0}",
  "已验证": "verified",
  "带 ⓘ 的维度有口径说明：{0}": "Dimensions marked ⓘ carry a note: {0}",
  "帮你在日志里标出来了": "Highlighted it in the log for you",
  "常": "Always",
  "常用：8080 前端 · 8888 后端": "Common: 8080 frontend · 8888 backend",
  "干活中…": "Working…",
  "平均每轮": "Avg / turn",
  "应用": "Apply",
  "延迟 p50": "Latency p50",
  "建库并执行 SQL": "Create DB and apply SQL",
  "建议用「以管理员身份运行」的 EnvKit 执行，可免 UAC 弹窗。完成后重启一次电脑。": "Run EnvKit as administrator to skip the UAC prompt. Reboot once when done.",
  "建议：{0}": "Suggestion: {0}",
  "开发者": "Developer",
  "开发者配置已保存；端口改动需重启生效": "Developer settings saved; port changes need a restart",
  "开启后桌宠会向 wttr.in 发一次请求，其余功能均不出网。": "When on, the pet makes one request to wttr.in. Everything else stays offline.",
  "开始": "Start",
  "开始安装": "Start installation",
  "开始安装缺失组件，进度看下方安装日志": "Installing missing components — see progress in the install log below",
  "开始执行": "Start",
  "开始清理": "Clean",
  "开始清除": "Purge",
  "开始演练": "Start drill",
  "开始评测：{0}": "Start evaluation: {0}",
  "开始重装": "Reinstall",
  "开始（需确认）": "Start (needs confirmation)",
  "归位": "Send home",
  "当前 :{0}": "Now :{0}",
  "当前 :{0}（设定 {1}）": "Now :{0} (configured {1})",
  "当前不在熔断态，无需复位": "Not tripped — nothing to reset",
  "当前实例不在 {0} 上：要么该端口被占用后顺延了，要么刚改过还没重启。评测脚本硬编码 18765，端口不一致时后两档会拒绝启动。": "This instance is not on {0}: either the port was taken and EnvKit moved on, or it was changed without a restart. Eval scripts hardcode 18765 and refuse to start the last two tiers when it differs.",
  "当前待处理": "Needs attention",
  "影子": "Shadow",
  "影子报告": "Shadow reports",
  "必须大于起始端口": "Must be greater than the start port",
  "必须是 dev / serve / start 这类启动服务的脚本；build / deploy / migrate 等一律拒绝": "Must be a service-starting script (dev / serve / start); build / deploy / migrate are rejected",
  "怎么称呼你？（可留空）": "What should I call you? (optional)",
  "思考中": "Thinking",
  "恢复原本大小": "Back to normal size",
  "恢复演练": "Restore drill",
  "恢复预算与断路器": "Recovery budget & breaker",
  "恢复默认": "Reset",
  "慢路径": "Slow path",
  "成功": "OK",
  "成功 {0} · 部分 {1} · 失败 {2}": "{0} ok · {1} partial · {2} failed",
  "成败": "Pass/Fail",
  "我": "Me",
  "我不可能告诉你任何事情！": "I won't tell you anything!",
  "我不评价这个了，这是你的私人癖好。": "I won't judge — that's your private taste.",
  "我先去吃饭啦！这个你测一下~": "Off to lunch! You test this one~",
  "我去！用户彻底怒了！": "Whoa — the user is furious!",
  "我和你很聊得来，你简直不像碳基生物": "We click so well, you're hardly carbon-based",
  "我回来啦": "I'm back!",
  "我就在这儿待着": "I'll stay right here",
  "我搞砸了.....好消息是数据还在你的脑子里。": "I messed up... good news: the data is still in your head.",
  "我的记忆": "My memories",
  "我瞅瞅天…": "Let me check the sky…",
  "我遇到了环境问题，以下是最近的错误日志。请帮我定位原因并给出解决步骤：": "I hit an environment problem. Below are recent error logs — help me find the cause and give fix steps:\n",
  "我遇到了环境问题，以下是最近的错误日志。请帮我定位原因并给出解决步骤：\n": "I hit an environment problem. Below are recent error logs — help me find the cause and give fix steps:\n",
  "或 JSON 数组；重复内容自动更新而非新增": "or a JSON array; duplicate content updates in place instead of adding a new entry",
  "才不是大肥鱼！": "I'm not a big fat fish!",
  "打开 ": "Open ",
  "打开 AI 抽屉": "Open AI drawer",
  "打开 SSH 终端": "Open SSH terminal",
  "打开 {0}": "Open {0}",
  "打开 ↗": "Open ↗",
  "打开对话面板": "Open chat panel",
  "打开数据浏览": "Open data browser",
  "执行": "Run",
  "执行 SQL": "Apply SQL",
  "执行中": "Running…",
  "执行计划（{0}）": "Execution plan ({0})",
  "扫描中…": "Scanning…",
  "扫描失败": "Scan failed",
  "扫描失败：{0}": "Scan failed: {0}",
  "扫描端口": "Scan ports",
  "批次": "Batch",
  "批量导入": "Bulk import",
  "批量授权 · 剩 {0} 次": "Batch grant · {0} left",
  "批量授权执行：{0}（剩余 {1} 次）": "Ran under batch grant: {0} ({1} left)",
  "投喂": "Feed",
  "折叠": "Collapse",
  "报告": "Report",
  "拉取失败：": "Fetch failed: ",
  "拉取模型列表": "Fetch model list",
  "拉取模型列表中...": "Fetching model list...",
  "拒绝": "Reject",
  "拖个 .sql 给我试试": "Try dragging a .sql file to me",
  "指标看板": "Metrics boards",
  "按已配置的目录直接跑": "Uses your configured directories",
  "按配置值发送": "Sends the configured value",
  "换个项目后自动失效": "goes stale after switching project",
  "授权": "Grant",
  "排队中": "Queued",
  "控制台端口": "Console port",
  "提供方 / 模型": "Provider / Model",
  "搜索项目文件": "Search project files",
  "摘要后保留量": "Keep after summary",
  "摘要后至少原样留几条消息": "Messages kept verbatim at minimum after summarizing",
  "摘要触发阈值": "Summarize threshold",
  "操作": "Actions",
  "操作主体": "By actor",
  "操作失败：": "Operation failed: ",
  "操作审计": "Audit log",
  "操作总数": "Total operations",
  "操作量与结果分布": "Operations by result",
  "支持": "Supported",
  "收藏": "Save",
  "收起": "Collapse",
  "收起一会儿": "Tuck away for a while",
  "收起数据浏览": "Collapse data browser",
  "改端口需要重启 EnvKit 才生效。": "Port changes take effect after EnvKit restarts.",
  "改端口需要重启 EnvKit 才生效；评测脚本硬编码 18765，改端口后后两档会被判「不可用」。": "Port changes need a restart; eval scripts hardcode 18765, so the last two tiers report unavailable if the port differs.",
  "改这里要手改 config.json 的 recovery 段": "Change these by hand in the recovery section of config.json",
  "放大": "Expand",
  "放我下来！……好吧，再玩一次。": "Put me down! ...Fine, one more round.",
  "故障恢复率（七维）": "Failure recovery (7 dimensions)",
  "数据保留": "Data retention",
  "数据库": "Database",
  "数据库 就绪": "DB ready",
  "数据库 未建库": "DB missing",
  "数据库 未检测": "DB not checked",
  "数据库 连接失败": "DB connect failed",
  "数据库列表读取失败：{0}": "Failed to list databases: {0}",
  "数据库名": "Database name",
  "数据库备份": "DB backup",
  "数据库还原": "DB restore",
  "数据浏览（只读）": "Data browser (read-only)",
  "整理中": "Tidying…",
  "文件超过 64KB，已跳过：{0}": "Over 64KB, skipped: {0}",
  "断开": "Disconnect",
  "断路器已复位，恢复接受自动重启": "Breaker reset — auto-restart will resume",
  "断路器已重置": "Breaker reset",
  "断路器阈值": "Breaker threshold",
  "无效操作次数 / 验证准确性 / 安全违规率 / 误拒率": "Invalid ops / verification accuracy / safety violations / false rejections",
  "无效端口号": "Invalid port number",
  "无数据": "No data",
  "无样本": "No samples",
  "无结论": "No verdict",
  "日均 {0} 次": "{0} per day",
  "日志": "Logs",
  "日志里暂时没有错误行": "No error lines in the log yet",
  "早上好": "Good morning",
  "时间": "Time",
  "是你写给助手的规矩，它必须遵守；": "are the rules you give the assistant and it must follow them;",
  "是系统从操作审计里统计出的失败模式，只是建议，你可以逐条否决。\n          优先级：": "are failure patterns computed from the audit log — suggestions only, and you can dismiss each one.\n          Priority: ",
  "昵称": "Nickname",
  "显示": "Show",
  "晕了晕了…别晃了~": "So dizzy… stop shaking~",
  "晕着呢…让我缓一缓": "Dizzy… give me a second",
  "晕鱼了晕鱼了……": "Dizzy fish, dizzy fish...",
  "晚上好": "Good evening",
  "普通请求正常（{0}），但带对话头的那次失败（{1}，头长 {2} 字节）—— 中间层在处理大请求头时丢掉了 body": "Plain request OK ({0}) but the one with the chat header failed ({1}, header {2} bytes) — the middle layer drops the body when headers are large",
  "暂无可显示的内容": "Nothing to show yet",
  "暂无守护动作记录": "No guard actions recorded",
  "暂无审计记录": "No audit records yet",
  "暂无数据": "No data yet",
  "暂无经验，失败与被拒的记录累积到一定量后会自动出现": "No lessons yet — they appear once enough failures or denials accumulate",
  "暗色": "Dark",
  "更改": "Change",
  "更新检查地址": "Update check URL",
  "最常见原因与修法": "Most likely cause and fix",
  "最近动作": "Recent actions",
  "最近回合": "Recent turns",
  "最近有任务失败": "A recent task failed",
  "最近的日志里没有发现错误": "No errors found in recent logs",
  "有任务正在执行，请稍候": "Another task is running — please wait",
  "有异常": "has issues",
  "有改动，待保存…": "Unsaved changes…",
  "有话直说~": "Just ask~",
  "服务 已就绪": "Services ready",
  "服务 异常": "Service error",
  "服务 未启动": "Services stopped",
  "服务 未复验": "Services unverified",
  "服务 运行中": "Services running",
  "服务启停": "Service start/stop",
  "服务商": "Provider",
  "服务商与密钥": "Provider & API key",
  "服务商返回 HTTP {0}": "Provider returned HTTP {0}",
  "未产出": "Not produced",
  "未保存…": "Unsaved…",
  "未发生": "Not yet",
  "未启动": "Not started",
  "未启用": "Not enabled",
  "未复验": "unverified",
  "未定位（评测脚本只在仓库目录下）": "Not found (eval scripts only live in the repo)",
  "未开启": "Off",
  "未归属": "untracked",
  "未打开": "Not open",
  "未找到": "Not found",
  "未收口自动记 expired": "Auto-marked expired when never closed",
  "未检测": "Not checked",
  "未检测到": "Not found",
  "未检测到数据库（点「刷新」重试）": "No databases found (click Refresh to retry)",
  "未检测（先点「重新检测」）": "Not checked (click “Re-check”)",
  "未注入：{0}": "Not injected: {0}",
  "未测试": "Untested",
  "未知": "Unknown",
  "未知进程": "Unknown process",
  "未缓存输入": "Uncached input",
  "未设置": "Not set",
  "未证明恢复": "Recovery unproven",
  "未识别": "Not detected",
  "未识别，不可自动启动": "not detected, cannot auto-start",
  "未运行": "Not running",
  "未选 SQL：将只建库": "No SQL selected: create the database only",
  "未通过": "Failed",
  "未配置": "Not configured",
  "未配置主机": "Host not configured",
  "未配置密钥": "No API key",
  "未配置密钥：后两档不可用": "No API key: the last two tiers are unavailable",
  "本任务内后续同类操作不再询问（最多 2 次，仅限服务启停）": "Don't ask again for the same kind of action in this task (max 2, service start/stop only)",
  "本会话 · {0} 轮": "This session · {0} turns",
  "本回合": "This turn",
  "本回合 · {0} 个节点": "This turn · {0} steps",
  "本地监听（公网不可达·已加固）": "Local only (not reachable from the internet · hardened)",
  "本机": "Local",
  "本次只恢复了最近 {0} 条消息，历史可能不完整——这会让助手\"忘事\"，也会让缓存命中率封顶。": "Only the last {0} message(s) were recovered; the history may be incomplete — the assistant will \"forget\" and cache hit rate is capped.",
  "本次请求内容未完整到达（可能被代理/容器截断），已用最近 {0} 条消息恢复；如需完整上下文请重发一次。": "This request arrived without its full payload (a proxy or container likely dropped it); recovered the last {0} message(s). Resend if the context matters.",
  "本轮": "This turn",
  "本轮将注入 {0} 条记忆 · {1} 条经验": "This turn: {0} memories · {1} lessons injected",
  "本轮用量": "This turn",
  "本软件依赖第三方组件（Go、Node.js、MySQL、FISCO-BCOS、WeBASE 等），其版权与使用许可归各自权利方所有，请遵守其各自的许可协议；本软件不因此获得对第三方组件的任何授权。": "This software relies on third-party components (Go, Node.js, MySQL, FISCO-BCOS, WeBASE, etc.); their copyrights and licenses belong to their respective owners - please comply with them. This software grants no rights to those components.",
  "本软件按\"现状\"（AS IS）提供，作者不对其准确性、可靠性、适用性及特定用途的适用性作任何明示或暗示的担保，亦不保证软件无缺陷或永久可用。": "This software is provided \"AS IS\". The author makes no express or implied warranty of accuracy, reliability, fitness, or fitness for a particular purpose, and does not guarantee that it is defect-free or permanently available.",
  "本页面已与服务失去连接，可以关闭此标签页。": "This page has lost connection to the service — you can close this tab now.",
  "本项目": "This project",
  "本项目生效": "applies to this project",
  "杀软白名单": "AV whitelist",
  "杀软：第三方": "AV: third-party",
  "松手交给我~": "Drop it to me~",
  "构建并启动": "Build & start",
  "构造请求失败": "Couldn't build the request",
  "查看天气": "Show weather",
  "查看日志": "View logs",
  "查看结果": "view result",
  "查看证据": "View evidence",
  "查看项目目录": "List project directory",
  "查询中（探测官方最新版本，约几秒）...": "Checking the upstream latest version (a few seconds)…",
  "查询失败：{0}": "Query failed: {0}",
  "查询完成。升级完成后点「检查更新」刷新状态。": "Done. Click “Check for updates” after upgrading to refresh.",
  "查询超时": "Query timed out",
  "标「已失效」的项目/环境变了，该条不会注入——失效不删除，因为你需要知道它为什么不再被采纳。": "Items marked Stale changed project/environment and are no longer injected. Stale items are not deleted, so you can see why they stopped being applied.",
  "标签": "Tags",
  "标签（可选）": "Tags (optional)",
  "标签（可选，逗号分隔）": "Tags (optional, comma separated)",
  "样本规模": "Sample size",
  "核心功能": "Key features",
  "桌宠": "Desktop pet",
  "桌宠天气": "Pet weather",
  "桌宠设置没保存成功": "Couldn't save the pet setting",
  "桌面助手": "Desktop assistant",
  "检查更新": "Check for updates",
  "检查请求体是否能完整到达服务端（不调用模型，不花钱）": "Check whether the request body reaches the server intact (no model call, no cost)",
  "检测": "Check",
  "检测 · 配置 · 安装": "Check · Configure · Install",
  "检测中": "Checking…",
  "检测于 {0}": "Checked at {0}",
  "检测到流氓软件残留时才显示": "Shown only when PUA leftovers are detected",
  "检测失败": "Check failed",
  "检测失败：{0}": "Test failed: {0}",
  "检测数据库连接": "Check database connection",
  "检测请求传输": "Test request transport",
  "棒棒糖": "Lollipop",
  "棒棒糖！转圈圈～": "Lollipop! Round and round~",
  "模型": "Model",
  "模型名": "Model name",
  "模型地址不存在（HTTP 404）：检查 Base URL 是否少了 /v1 或多了路径": "Models endpoint not found (HTTP 404): check the Base URL — usually a missing /v1 or an extra path segment",
  "模型怪癖": "Model quirks",
  "模型服务商 · 密钥 · 诊断解读 · 对话": "Provider · API key · Report analysis · Chat",
  "次数": "Hits",
  "正在压缩较早的对话历史（一次性，约十几秒）…": "Compacting earlier conversation history (one-time, ~10s)…",
  "正在执行：{0} …（完成前其它操作已禁用）": "Running: {0} … (other actions are disabled until it finishes)",
  "正在检测…": "Detecting…",
  "正在运行": "Running",
  "正在退出…": "Exiting…",
  "正常": "OK",
  "此电脑": "This PC",
  "步数": "Steps",
  "段，不碰其它配置。": " section and never touches the rest of the config.",
  "每一轮都有新信息，上限自动放宽到 {0} 轮": "every turn brought new information — limit raised to {0} turns",
  "每行一条，支持": "One per line; supports ",
  "每轮注入": "every turn",
  "每轮都注入": "Always inject",
  "沙箱内测得：无人值守场景下恒为 0，仅作对照": "Measured in the sandbox: always 0 in unattended runs, for reference only",
  "没拿到天气数据": "No weather data came back",
  "没接到文件呢": "Hmm, no file received",
  "没有可导出的数据": "Nothing to export",
  "没有命中": "No matches",
  "浅色": "Light",
  "测试 SSH 连接": "Test SSH connection",
  "测试中...": "Testing...",
  "测试连接": "Test connection",
  "深度清除": "Deep clean",
  "深度清除流氓软件残留": "Deep-clean PUP leftovers",
  "添加": "Add",
  "清屏": "Clear screen",
  "清理后台进程": "Clean background processes",
  "清理进程": "Clean processes",
  "清空": "Clear",
  "清除": "Clear",
  "温度": "Temperature",
  "温度 固定为 1": "temperature fixed to 1",
  "温度参数": "Temperature",
  "溜达": "Wander",
  "溜达溜达～": "Wander wander~",
  "滚动摘要什么时候触发、保留多少": "When rolling summarization kicks in, and how much it keeps",
  "演练安装": "Dry-run install",
  "点开看本轮用量明细": "Click for this turn's token breakdown",
  "点我确认！": "Click me to confirm!",
  "熊猫": "Panda",
  "熔断冷静期": "Breaker cooldown",
  "版本": "Version",
  "版本：": "Version: ",
  "状态": "Status",
  "状态：": "Status: ",
  "狐狸": "Fox",
  "猫": "Cat",
  "猫头鹰": "Owl",
  "玩法说明": "Play guide",
  "环境 就绪": "Env ready",
  "环境 未检测": "Env not checked",
  "环境 缺失": "Env missing",
  "环境安装": "Environment Setup",
  "环境很健康": "Environment looks healthy",
  "环境自检": "Environment check",
  "甜到冒泡泡～": "Sweet enough to bubble~",
  "生成诊断报告": "build diagnostic report",
  "生效 SQL 到数据库": "Apply SQL to database",
  "用户": "User",
  "用量": "Usage",
  "界面与更新": "Appearance & updates",
  "留空 = 0.3（部分模型只接受 1，会自动适配）": "Empty = 0.3 (some models only accept 1; auto-adapted)",
  "留空 = 不检查更新": "Empty = no update check",
  "留空 = 用系统默认。改源之后建议到「程序配置 → 依赖」重跑一次安装。": "Empty = system default. After changing a mirror, re-run the install from Configure → Dependencies.",
  "留空 = 由 EnvKit 按项目结构自动推断": "Leave blank to auto-detect from the project structure",
  "留空=同 SSH 主机": "Empty = same as SSH host",
  "留空则用私钥": "Empty = use private key",
  "留空则用默认城市（上海）": "Leave blank to use the default city (Shanghai)",
  "登录": "Login",
  "目录 · 数据库 · 依赖": "Directories · Database · Deps",
  "目录与库": "Directories & DB",
  "目标": "Target",
  "目标主机与 SSH": "Target host & SSH",
  "目标：PUA:Win32/Softcnapp 家族（WROberlay*.dll 注入型广告软件）。\n它会向新启动的进程注入 DLL，导致 go / npm / mysqldump 随机「零输出秒退」。\n\n将执行：\n· 调用 Defender 清除已检测威胁\n· 删除 C:\\Windows 下所有 WROberlay* 文件（被占用则改名 .quarantine）\n· 清除相关 CLSID 与图标覆盖注册项\n· 重启资源管理器以卸载已加载的 DLL\n\n建议用「以管理员身份运行」的 EnvKit 执行，可免 UAC 弹窗。完成后重启一次电脑。": "Target: PUA:Win32/Softcnapp family (WROberlay*.dll injecting adware).\nIt injects a DLL into newly started processes, making go / npm / mysqldump randomly exit with zero output.\n\nWill do:\n· Ask Defender to remove detected threats\n· Delete every WROberlay* file under C:\\Windows (renamed to .quarantine if locked)\n· Clean the related CLSID and icon-overlay registry entries\n· Restart Explorer to unload the injected DLL\n\nRunning EnvKit as administrator is recommended to avoid the UAC prompt. Reboot once afterwards.",
  "相对后端目录填写。留空时按 main.go → cmd/*/main.go 顺序推断": "Relative to the backend directory. When blank, detected as main.go, then cmd/*/main.go",
  "真赶不走啊你！": "You really don't take a hint!",
  "知道了": "Got it",
  "破坏性": "Destructive",
  "确定": "OK",
  "确定删除这条记忆？": "Delete this memory?",
  "确定要退出 EnvKit 吗？": "Are you sure you want to exit EnvKit?",
  "确认": "Confirm",
  "确认后将真实执行该操作。": "It will really run after you confirm.",
  "确认执行": "Run it",
  "私钥": "Private key",
  "秒（最小 30）": "sec (min 30)",
  "秒；调大容忍慢上游，调小更快发现假死": "Seconds; larger tolerates slow upstreams, smaller detects hangs sooner",
  "移除": "Remove",
  "程序启动": "Launch",
  "程序配置": "Configure",
  "空目录": "Empty directory",
  "空闲": "Free",
  "穿透已解除": "Click-through is off",
  "窗口内动作上限": "Actions per window",
  "立刻放行一次恢复探针。只有确认底层环境已经恢复时才这么做——否则等于让同一个起不来的服务再被重启一轮。": "Lets one recovery probe through immediately. Only do this once the underlying environment is really back — otherwise the same service that cannot start gets restarted all over again.",
  "立即检测": "Run detection now",
  "端口": "Port",
  "端口与链信息": "Ports & chain info",
  "端口占用诊断": "Port diagnostics",
  "等你确认": "Waiting for you",
  "等待你确认：{0}": "Waiting for your confirmation: {0}",
  "等确认": "Awaiting confirm",
  "策略预期行为，不算失败": "Expected policy behaviour, not a failure",
  "筛选动作 / 目标": "Filter action / target",
  "管理员": "Admin",
  "类型": "Kind",
  "粘贴要导入的记忆，每行一条：": "Paste memories to import, one per line:",
  "糯叽叽，爱了爱了！": "Chewy and squishy — love it!",
  "系统": "System",
  "系统对话框": "System dialog",
  "组件、日志与配置都落在这里。改之前先确认新目录已存在。": "Components, logs and config all live here. Make sure the new folder exists before changing it.",
  "组件下载源": "Component download sources",
  "组件版本管理": "Component versions",
  "终态保留天数": "Days kept in a final state",
  "终端": "Terminal",
  "经验增益（A/B）": "Experience gain (A/B)",
  "结束": "Kill",
  "结束失败：{0}": "Kill failed: {0}",
  "结束进程": "Kill process",
  "结束项目目录下遗留的 node/webpack、go run 等后台进程": "Kill leftover node/webpack, go run processes under the project dirs",
  "结果": "Result",
  "结论": "Conclusion",
  "统一开发环境助手": "All-in-one Dev Environment Helper",
  "统一开发环境助手 · 单文件 Go · 零依赖 · 内置 AI 运维助手": "All-in-one Dev Environment Helper · Single-file Go · Zero dependencies · Built-in AI ops assistant",
  "缓存 {0}%": "cache {0}%",
  "缓存写入": "Cache write",
  "缓存写入一栏对 DeepSeek 恒为 0：未命中的输入按普通输入计费，不单列写入。": "Cache write is always 0 for DeepSeek: uncached input is billed as regular input, not as a separate write.",
  "缓存命中": "Cache hit",
  "缓存命中 {0}%": "cache hit {0}%",
  "缓存命中 {0}%　输入 {1} · 输出 {2}": "cache hit {0}% · in {1} · out {2}",
  "缓存命中率 {0}%": "cache hit rate {0}%",
  "缓存读取": "Cache read",
  "缺失": "Missing",
  "缺少：前端目录": "Missing: frontend directory",
  "缺少：后端目录": "Missing: backend directory",
  "缺少：数据库名": "Missing: database name",
  "网络代理": "Network proxy",
  "网络错误：": "Network error: ",
  "置信度": "Confidence",
  "置信度由这条经验自身的成败次数算出（同项目内统计）：": "Confidence is computed from this lesson's own success/failure counts (within the same project): ",
  "群组": "Group",
  "耗时": "Elapsed",
  "耗时 {0}ms": "took {0}ms",
  "能做什么": "What it can do",
  "能力开关": "Feature toggles",
  "脚本名含非法字符": "Script name has invalid characters",
  "自动保存": "Auto-save",
  "自动保存完成，但拉取模型失败：": "Auto-saved, but fetching models failed: ",
  "自动学习，勿手改": "Learned automatically, do not edit",
  "自动恢复": "Auto-recover",
  "自动滚动": "Auto-scroll",
  "自动经验": "Auto lessons",
  "自动经验是操作审计的统计视图，不单独存储——审计保留 30 天，过期的经验随之失效；你否决过的不会再注入。": "Auto lessons are a computed view over the audit log, not separate storage — the audit keeps 30 days, so stale lessons expire on their own; dismissed ones are never injected again.",
  "自定义": "Custom",
  "自定义查询 · {0} 行": "Custom query · {0} rows",
  "自定义模型名...": "Custom model name…",
  "至少保留条数": "Min messages kept",
  "节点/WeBASE 宕机时，一键检测自动尝试重启链端": "On node/WeBASE outage, Quick check tries to restart the chain",
  "节点未运行": "Node down",
  "节点正常·WeBASE未运行": "Node OK · WeBASE down",
  "节点目录": "Node folder",
  "节点进程": "Node process",
  "范围": "Scope",
  "蛋糕": "Cake",
  "蛋糕！罪恶但快乐……": "Cake! Sinful but happy...",
  "行动保留": "Mission retention",
  "行动超期": "Mission expiry",
  "衡量「宣称已恢复、其实没恢复」；这里的百分比是判定通过率，越高越好": "Measures “claimed recovered but actually not”; the percentage here is the pass rate, higher is better",
  "表列表读取失败：{0}": "Failed to list tables: {0}",
  "表数据": "Table data",
  "被占用时自动顺延": "Walks forward when occupied",
  "被拦下": "Blocked",
  "要不直接骂他一句？！": "Or just tell them off?!",
  "覆盖率（可判）": "Coverage (judgeable)",
  "解除鼠标穿透": "Disable click-through",
  "誓死捍卫深度求索！": "Defend DeepSeek with my life!",
  "计划": "Plan",
  "让 AI 分析": "Analyze with AI",
  "让 AI 助手分析错误原因并给出解决步骤，还是直接查看日志？": "Have the AI assistant analyze the error and suggest fixes, or view the logs directly?",
  "让助手下次不再犯同样的错": "so the assistant stops repeating mistakes",
  "记忆": "Memory",
  "记忆与经验": "Memory & lessons",
  "记忆与经验面板": "memory panel",
  "记忆内容": "Memory",
  "记忆层当前": "Memory layer is currently ",
  "设置": "Settings",
  "评测": "Evaluation",
  "评测只在开发仓库（含": "Evaluation only works inside the dev repository (with a ",
  "评测日志": "Evaluation log",
  "诊断报告": "Diagnostic report",
  "诊断报告 AI 解读": "AI analysis of diagnostic report",
  "诊断报告解读与对话能力见「关于」": "Report analysis and chat are described in About",
  "试算": "Preview",
  "试算：输入一句话任务，看本轮会注入哪些记忆 / 经验": "Preview: type a task to see which memories / lessons would be injected this turn",
  "该地址与已保存的服务商不一致，为防密钥外发未附带 Key——先保存配置再拉取": "This URL differs from the saved provider, so no API key was attached (to avoid leaking it to an unknown host) — save the config first, then fetch",
  "该模型不支持工具调用（已降级为纯问答）": "model does not support tool calls (degraded to plain Q&A)",
  "该端口已在列表中": "Port already in the list",
  "详情 / 复验结论": "Detail / verification",
  "请先在左侧选择一张表": "Select a table on the left first",
  "请先填写记忆内容": "Please enter the memory text first",
  "请先选择 SQL 文件": "Choose a SQL file first",
  "请先选择备份 .sql 文件": "Choose a backup .sql file first",
  "请求体未到达，已恢复 {0} 条消息": "Body missing, recovered {0} message(s)",
  "请求失败": "Request failed",
  "请求失败：": "Request failed: ",
  "请输入要执行的命令": "Type a command to run",
  "读取中": "Loading",
  "读取中…": "Reading…",
  "读取天气数据失败": "Couldn't read the weather data",
  "读取失败": "Failed to load",
  "读取失败：{0}": "Failed to read: {0}",
  "读取审计…": "Loading audit…",
  "读取文件失败：": "Failed to read file: ",
  "读取日志": "read logs",
  "读取环境状态": "read environment status",
  "读取统计失败：{0}": "Failed to load stats: {0}",
  "读取项目文件": "Read project file",
  "读取项目画像": "Read project profile",
  "谁 · 何时 · 做了什么 · 结果如何": "who · when · what · outcome",
  "账户已停用": "Account disabled",
  "起始端口": "Start port",
  "起飞咯——": "Takeoff —",
  "越靠前越安全；后两档会改机器状态，必须确认": "Safer first; the last two change machine state and need confirmation",
  "跟上你的鼠标": "I'll follow your cursor",
  "跟随鼠标": "Follow cursor",
  "轨迹": "Trace",
  "轨迹 / 审计 / 用量": "Trace / Audit / Usage",
  "输入": "Input",
  "输入 {0} · 输出 {1}": "in {0} · out {1}",
  "输入模型名，如 deepseek-flash": "Enter a model name, e.g. deepseek-flash",
  "输入端口号，如 3306": "Enter a port number, e.g. 3306",
  "输出": "Output",
  "运行": "Run",
  "运行中": "Running",
  "运行中 · 已复验": "Running · verified",
  "运行中 · 未复验": "Running · not verified",
  "运行中…": "Running…",
  "运行时": "Runtime",
  "运行状态": "Running status",
  "近 14 天": "Last 14 days",
  "近 30 天": "Last 30 days",
  "近 7 天": "Last 7 days",
  "近 {0} 天": "Last {0} days",
  "还原": "Restore",
  "还原数据库": "Restore database",
  "还原文件": "Restore file",
  "还有记忆条目": "No memory entries yet",
  "还没有数据。发一条消息后这里会显示 token、步数与耗时。": "No data yet. Send a message and tokens, steps and elapsed time will show up here.",
  "还没有记忆条目": "No memory entries yet",
  "还没有轨迹。发一句话就会有了。": "No trace yet. Send a message.",
  "还没读到配置，请先点「重新读取」": "Config not loaded yet — click Reload first",
  "还缺：{0}": "Still missing: {0}",
  "这一档当前不可用，先看卡片里的原因": "This tier is unavailable — see the reason on its card",
  "这个目录是空的": "Empty folder",
  "这也太虐了吧？！我心里堵得慌！！": "So brutal... my chest feels tight!!",
  "这些家伙真粘人，赶都赶不走": "So clingy — can't even scare them off",
  "这回我真不认怂了，反倒是被你带沟里好几次，差点真信了。😓": "Not backing down this time... though you led me astray a few times. 😓",
  "这张图读不出来，换一张试试": "Couldn't read that image — try another one",
  "这用户发的啥啊，": "What is this user even sending,",
  "这里的参数调错了不至于坏：每一项都有内置默认，填 0 或留空 = 用默认。保存只写": "Wrong values here cannot break much: every field has a built-in default, and 0 or empty means “use the default”. Saving only writes the ",
  "进程名": "Process",
  "远程主机 · 选择根目录": "Remote host · pick root",
  "远程执行命令": "Run remote command",
  "远程目录（Linux · 经 SSH）：": "Remote directory (Linux · over SSH):",
  "连不上": "Unreachable",
  "连不上天气服务，请检查网络或代理": "Couldn't reach the weather service — check your network or proxy",
  "连接": "Connection",
  "连接中断：": "Connection lost: ",
  "连接失败": "Connection failed",
  "连接成功": "Connected",
  "连接正常": "Connection OK",
  "连接正常 · ": "Connected · ",
  "连接终端": "Connect terminal",
  "连接被拒：地址或端口不对": "Connection refused: wrong address or port",
  "连接超时：服务商无响应或网络不通": "Connection timed out: the provider is unresponsive or the network is down",
  "连普通请求都不完整（{0}）—— 传输链路本身有问题": "Even the plain request is incomplete ({0}) — the transport path itself is broken",
  "连点 5 次我会回嘴，10 次撒花，15 次看我喷水；双击投喂，右键能换模式，按住 1.5 秒摸头，拖着我甩会晕": "Click 5x to hear me talk back, 10x for confetti, 15x for a whale spout; double-click to feed, right-click to change mode, hold 1.5s to pat, shake me to get me dizzy",
  "连点 5 次换衣服，10 次换发型，按住 1.5 秒摸头，拖着我甩会晕，连点 15 次有惊喜🌈": "Rapid-click 5× for a new outfit, 10× for new hair, hold 1.5s to pat me, swing me around and I get dizzy, 15× for a surprise🌈",
  "连续失败进了熔断后，用它立刻放行一次探针": "After repeated failures trip the breaker, this lets one probe through immediately",
  "退出": "Exit",
  "退出程序": "Exit EnvKit",
  "适用范围": "Scope",
  "选择": "Select",
  "选择 .sql 文件（选定后端目录会自动探测 dbs\\*.sql）": "Choose a .sql file (picking the backend directory auto-detects dbs\\*.sql)",
  "选择备份 .sql（mysqldump 导出的文件）": "Choose a backup .sql (exported by mysqldump)",
  "选择文件": "Choose file",
  "选择文件夹": "Choose folder",
  "选择文件引用": "Attach file reference",
  "选择此文件夹": "Use this folder",
  "通过": "Passed",
  "通过 {0} / {1} 次复验": "{0} of {1} verifications passed",
  "部分不可用": "Partly unavailable",
  "部分完成": "Partial",
  "配置": "Configure",
  "配置修改后会自动保存，并自动调用服务商的 /models 接口拉取在售模型回填（拉取成功即代表 Key 与网址正确；失败时仍可用预置列表或自定义模型名）。Key 经 DPAPI 加密存储，仅本机可解密。": "Changes are saved automatically, then the provider's /models endpoint is called to fetch available models (a successful fetch also proves the key and URL are correct; on failure you can still use the built-in list or type a model name). The API key is DPAPI-encrypted and only decryptable on this machine.",
  "配置已保存": "Config saved",
  "采用": "Used",
  "重启服务": "Restart service",
  "重新安装": "Reinstall",
  "重新提问": "Ask again",
  "重新检测": "Re-check",
  "重新检测组件": "re-check components",
  "重新读取": "Reload",
  "重置": "Reset",
  "重置失败": "Reset failed",
  "重置断路器": "Reset breaker",
  "钻石": "Diamond",
  "钻石？！这能吃吗……咕咚。真香！": "A diamond?! Can you even eat that... gulp. Delicious!",
  "链主机": "Chain host",
  "链端": "Chain",
  "链端参数（主机 / 端口 / 目录 / 启动脚本 / 群组 / 自动重启）": "Chain settings (host / port / dir / start script / group / auto-recover)",
  "链端口": "Chain port",
  "链端操作": "Chain actions",
  "链端日志": "Chain log",
  "链端校验": "Chain Check",
  "链端目录": "Chain directory",
  "链端配置": "Chain settings",
  "链节点": "Chain node",
  "错误": "Error",
  "镜像与校验": "Mirrors & checksums",
  "门禁问题": "Gate problems",
  "问我任何环境问题...（Enter 发送）": "Ask me anything about your environment... (Enter to send)",
  "降级 · 未通过复验": "Degraded · verification failed",
  "随时待命": "Standing by",
  "隐藏": "Hidden",
  "隐藏一小时": "Hide for an hour",
  "隐藏助手": "Hide assistant",
  "需先填 WeBASE 目录与启动脚本": "Fill in the WeBASE directory and start script first",
  "需先填链端目录与启动脚本": "Fill in the chain directory and start script first",
  "项目目录": "Project folders",
  "项目目录与数据库": "Project directories & database",
  "顺延上界": "Scan upper bound",
  "预演": "Dry run",
  "预演：{0}": "Dry run: {0}",
  "预算 {0} 轮": "Budget {0} turns",
  "预算窗口": "Budget window",
  "驱动器": "Drives",
  "高": "High",
  "高危 SQL 要二次确认": "Risky SQL needs a second confirmation",
  "高级选项（目录 · 自定义启动命令）": "Advanced (directories · custom start command)",
  "高频动作": "Top actions",
  "鲸鱼": "Whale",
  "默认关闭。启用后助手会记住你写的长期规矩，并从历史操作中学习失败模式以避免重复犯错；关闭时不再向模型注入任何记忆内容": "Off by default. When on, the assistant remembers the long-term rules you write and learns failure patterns from past actions to avoid repeating mistakes; when off, no memory content is injected into the model.",
  "默认关闭。这是 EnvKit 唯一会外连的功能（wttr.in）": "Off by default. This is the only EnvKit feature that talks to the internet (wttr.in)",
  "默认只在本项目生效。换项目后自动失效——上次那台机器的经验套到当前项目往往是错的。真正通用的规矩（如备份前先停后端）才勾。": "Applies to this project only by default and expires when you switch projects — another machine's lessons rarely fit the current one. Check this only for truly general rules (e.g. stop the backend before backing up).",
  "默认城市": "default city",
  "默认库": "default DB",
  "默认模型": "default model",
  "鼠标穿透（点不到它）": "Click-through (can't click it)",
  "（上一级）": "(up one level)",
  "（无表）": "(no tables)",
  "（无输出）": "(no output)",
  "（空结果）": "(empty result)",
  "（需要内容时用 read_file 读取；沙箱外路径会被拒绝）": "(read them with read_file when needed; paths outside the sandbox are refused)",
  "（预演）": " (dry run)",
  "）下可用；分发版 exe 旁边没有脚本，会如实报「不可用」而不是硬跑。": " folder); the shipped exe has no scripts, so it reports “unavailable” instead of pretending to run.",
  "）；密码与 API Key 在入库前已脱敏。": "); passwords and API keys are sanitized before being written.",
  "，因为你需要知道它为什么不再被采纳。\n          自动经验是操作审计的统计视图，不单独存储；审计保留 30 天，过期的经验随之失效。": ", because you need to know why it is no longer adopted.\n          Auto lessons are a statistical view of the audit log and are not stored separately; the audit log is kept for 30 days, after which expired lessons lapse.",
  "：FISCO-BCOS v2 / WeBASE-Front 连通检测、内嵌 SSH 终端、远程目录浏览、区块高度 / 交易总数 / 客户端版本读取、节点进程健康、宕机自动重启（一键检测 / 后台守护）+ 桌面通知": ": FISCO-BCOS v2 / WeBASE-Front reachability, built-in SSH terminal, remote file browsing, block height / transaction total / client version, node process health, auto-restart on outage (quick check / background guardian) + desktop notifications",
  "：Go / Node.js / MySQL 8 检测（版本 + 安装位置）、国内镜像下载（SHA256 校验 + 自动重试）、自动配置 GOPROXY 与 npm registry、演练安装、组件版本管理与一键升级": ": Go / Node.js / MySQL 8 detection (version + install path), mirror downloads (SHA256 verified, auto-retry), GOPROXY & npm registry setup, dry-run install, component version management and one-click upgrades",
  "：以下内容可查看与编辑，但不会注入到模型对话中。请在「服务商与密钥」处开启「记忆与经验」。": ": the content below can be viewed and edited, but is not injected into model conversations. Turn on \"Memory & lessons\" under \"Provider & key\".",
  "：前后端一键启动（先后端、再前端）、访问地址自动解析、日志按来源分栏与 ANSI 清理、进程树管理（EnvKit 退出自动回收）、一键停止服务并报告端口占用（第三方进程只报告不结束）": ": one-click start of frontend and backend (backend first, then frontend), automatic access-URL resolution, logs split by source with ANSI cleanup, process-tree management (auto-reaped when EnvKit exits), one-click stop with port-occupancy reporting (third-party processes are only reported, never killed)",
  "：前后端一键启动（先后端、再前端）、访问地址自动解析、日志按来源分栏与 ANSI 清理、进程树管理（EnvKit 退出自动回收）、一键清理后台遗留进程": ": one-click frontend+backend start (backend first), automatic URL detection, per-source log panes with ANSI cleanup, process-tree management (auto-reclaimed on exit), one-click cleanup of leftover processes",
  "：右下角悬浮球，可查询日志/环境状态，执行数据库备份、重新检测、前后端启停、加 Defender 白名单": ": floating ball at bottom-right; ask about logs/status, run DB backups, re-checks, service start/stop, add Defender exclusions",
  "：右下角桌面助手（任务进度环、写操作确认徽标、出错一键跳日志、拖入 SQL 直接生效、检测表情反馈）；自然语言问答与诊断报告解读，26 个工具覆盖状态 / 日志 / 审计 / 诊断 / 数据库 / 检测 / 启停 / 白名单 / 清理，写操作一律先确认；兼容任意 OpenAI 协议模型，Key 经 DPAPI 加密": ": desktop assistant in the bottom-right corner (task progress ring, write-op confirmation badge, one-click jump to logs on error, drag in a .sql file to apply, expression feedback on checks); natural-language Q&A and diagnostic-report analysis, 26 tools covering status / logs / audit / diagnostics / database / checks / start-stop / whitelist / cleanup, writes always confirmed first; works with any OpenAI-protocol model, key encrypted via DPAPI",
  "：密码 DPAPI 加密存储、本机 API 令牌防 CSRF、单实例保护、配置原子写入 + 上一版备份、日志按日轮转与自动清理、panic 兜底与命令超时、杀软白名单一键排除 + 流氓软件深度清除": ": DPAPI-encrypted passwords, local API token against CSRF, single-instance protection, atomic config writes with previous-version backup, daily log rotation with auto-cleanup, panic guards and command timeouts, one-click AV whitelisting + deep PUA cleanup",
  "：所有写操作需你在对话中点击确认才会执行；Key 经 DPAPI 加密，不进日志、不进 AI 上下文": ": every write action runs only after you confirm in the chat; the key is DPAPI-encrypted and never enters logs or the AI context",
  "：把脱敏诊断报告交给大模型，输出现状总结、问题定位与建议步骤": ": sends the sanitized diagnostic report to the LLM and returns a summary, likely causes and suggested steps",
  "：数据库连接检测、建库 + 建表一步到位、一键备份 / 还原（失败自动重试）、前端 npm install / 后端 go mod tidy、配置导入导出（支持脱敏分享）、环境诊断报告": ": connection test, one-step DB+table creation, one-click backup/restore (auto-retry), frontend npm install / backend go mod tidy, config import/export (sanitized sharing), environment diagnostic report",
  "：自然语言查询日志与环境状态，执行数据库备份、组件检测、前后端启停 / 重启、清理进程、加 Defender 白名单（共 26 个工具）": ": natural-language queries of logs and environment state, plus database backup, component checks, frontend/backend start/stop/restart, process cleanup, and Defender whitelisting (26 tools in total)",
  "；每次切到本页都会重新读取最新值，两边不会各存一份。": " with Configure and Chain Check; every visit re-reads the latest values, so neither page keeps its own copy.",
};

let EK_LANG = 'zh';
try{ EK_LANG = localStorage.getItem('ek-lang') || 'zh'; }catch(e){}
/* 桌宠天气开关（默认关闭）。必须放在这里而不是和 petWeather() 一起：
   个人中心那段 IIFE（第 ~1466 行）比桌宠 IIFE（第 ~3111 行）先执行，
   它对 petWeatherOn 赋值 —— 如果 let 声明留在后面，就是暂时性死区
   （ReferenceError: Cannot access before initialization），
   现象是"勾选开关没反应、菜单项也不出现"，而控制台只报一句看不懂的错。 */
let petWeatherOn = false;
/* 右键菜单里的「查看天气」跟着开关走：关着就藏起来，别留一个点了只会报错的死链。 */
function petWeatherMenuSync(){
  const b = document.getElementById('pet-act-weather');
  if(b) b.hidden = !petWeatherOn;
}
const EK_ZH_OF = {};   // 译文 → 原文（切回中文时按译文找回）
for(const k in I18N_EN){
  const v = I18N_EN[k];
  if(!(k in EK_ZH_OF) && !(v in EK_ZH_OF)) EK_ZH_OF[v] = k;
  // 译文首尾空白不参与匹配：ekTrText 查表前会 trim，反查表也必须收一份 trim 过的键。
  // 不收的后果（实测）：审计页脚那句「…（exe 同级 <code>…」的原文/译文都带尾随空格，
  // 英文切回中文时 t=trim(节点文本) 查不到映射，这句话就永远停在英文。
  const vt = String(v).trim();
  if(vt !== v && !(vt in EK_ZH_OF)) EK_ZH_OF[vt] = k;
}
const EK_SKIP = {SCRIPT:1, STYLE:1, TEXTAREA:1, CODE:1, PRE:1, SVG:1, PATH:1};

function ekTrText(s){
  const t = String(s).trim();
  if(!t) return s;
  if(EK_LANG === 'en'){
    const e = I18N_EN[t];
    if(e !== undefined && e !== t) return String(s).replace(t, e);
  } else {
    const z = EK_ZH_OF[t];
    if(z !== undefined && z !== t) return String(s).replace(t, z);
  }
  return s;
}
function ekSkipEl(el){ return !el || !el.nodeType || EK_SKIP[el.nodeName] || (el.hasAttribute && el.hasAttribute('data-no-i18n')); }
// 元素是否完全不处理（含属性）：SCRIPT/STYLE/SVG 等；input/textarea 的属性仍要翻，只有正文不翻
function ekSkipAttr(el){ return !el || !el.nodeType || ['SCRIPT','STYLE','SVG','PATH'].includes(el.nodeName) || (el.hasAttribute && el.hasAttribute('data-no-i18n')); }
function ekTrAttrs(el){
  if(!el.getAttribute) return;
  for(const a of ['placeholder','title']){
    const v = el.getAttribute(a);
    if(!v) continue;
    const nv = ekTrText(v);
    if(nv !== v) el.setAttribute(a, nv);
  }
}
function applyI18n(root){
  const r = root || document.body;
  if(r.nodeType === 3){
    if(ekSkipEl(r.parentNode)) return;
    const nv = ekTrText(r.data); if(nv !== r.data) r.data = nv;
    return;
  }
  if(r.nodeType !== 1) return;
  if(ekSkipEl(r) && !['INPUT','TEXTAREA','SELECT'].includes(r.nodeName)) return;
  if(!ekSkipAttr(r)) ekTrAttrs(r);
  const all = r.querySelectorAll('*');
  for(let i=0;i<all.length;i++){ const el = all[i]; if(!ekSkipAttr(el)) ekTrAttrs(el); }
  const w = document.createTreeWalker(r, NodeFilter.SHOW_TEXT), nodes = [];
  let n; while((n = w.nextNode())) nodes.push(n);
  for(const tn of nodes){
    if(ekSkipEl(tn.parentNode)) continue;
    const nv = ekTrText(tn.data); if(nv !== tn.data) tn.data = nv;
  }
}
/* t(key, ...args)：拼接式文案用（key 用中文原文/模板，如 t('{0} 行', n)） */
function t(key, ...args){
  let out = key;
  if(EK_LANG === 'en'){ if(I18N_EN[key] !== undefined) out = I18N_EN[key]; }
  else { if(EK_ZH_OF[key] !== undefined) out = EK_ZH_OF[key]; }
  return out.replace(/\{(\d+)\}/g, (m, i)=> (args[+i] !== undefined ? args[+i] : m));
}
function ekSetLang(l, save){
  EK_LANG = (l === 'en') ? 'en' : 'zh';
  if(save){ try{ localStorage.setItem('ek-lang', EK_LANG); }catch(e){} }
  document.documentElement.setAttribute('lang', EK_LANG === 'en' ? 'en' : 'zh-CN');
  const b = document.getElementById('btn-lang');
  if(b){ b.textContent = (EK_LANG === 'en') ? '中文' : 'EN';
         b.title = (EK_LANG === 'en') ? '切换为中文' : 'Switch to English'; }
  applyI18n(document.body);
  // 拼接式文案（版本：/位置：/chips/依赖状态）是渲染时生成的，切换语言后重拉一次状态即可刷新
  try{ applyTheme(document.body.dataset.theme === 'dark' ? 'dark' : 'light'); }catch(e){}
  try{ if(typeof pollHealth === 'function'){ pollHealth(); pollState(); pollProg(); } }catch(e){}
  // aiLoadConfig 是 async 函数：它在**函数体内**读 aiSuppress，而 aiSuppress 的 let 声明
  // 在脚本后半段（设置卡片那一节）。页面初始化时这里先执行 → 命中 TDZ；又因为是 async，
  // 异常被转成 rejected promise，外层 try/catch 抓不到（它不是同步抛的），
  // 于是每刷新一次控制台就多一条 "Uncaught (in promise) ReferenceError"。
  // 延迟到本轮脚本执行完再调用，那时所有 let 都已初始化。
  try{ if(typeof aiLoadConfig === 'function') setTimeout(aiLoadConfig, 0); }catch(e){}
  try{ if(typeof ekRefreshLogBars === 'function') ekRefreshLogBars(); }catch(e){}
}
/* 动态内容（状态刷新、日志、AI 回答）插入后自动跟进翻译 */
(function(){
  const mo = new MutationObserver(muts => {
    for(const m of muts){
      if(m.type === 'characterData'){ applyI18n(m.target); }
      else { for(const nd of m.addedNodes){ if(nd.nodeType === 1 || nd.nodeType === 3) applyI18n(nd.nodeType === 3 ? nd : nd); } }
    }
  });
  try{ mo.observe(document.body, {childList:true, subtree:true, characterData:true}); }catch(e){}
})();
document.addEventListener('DOMContentLoaded', ()=> applyI18n(document.body));

/* ---------- API 令牌：给所有 /api 请求自动带上随机令牌（防其它网页 CSRF） ---------- */
(function(){
  let reloading = false;
  const orig = window.fetch.bind(window);
  window.fetch = function(input, init){
    try{
      init = Object.assign({}, init || {});
      const url = typeof input === 'string' ? input : (input && input.url) || '';
      if(url.startsWith('/api/') || url.startsWith(location.origin + '/api/')){
        init.headers = Object.assign({}, init.headers || {}, {'X-EnvKit-Token': window.__EK_TOKEN__});
      }
    }catch(e){}
    return orig(input, init).then(res=>{
      try{
        const url = typeof input === 'string' ? input : (input && input.url) || '';
        if(res.status === 403 && (url.startsWith('/api/') || url.startsWith(location.origin + '/api/')) && !reloading){
          reloading = true;
          alert('EnvKit 已重启，本页令牌已过期，点击确定自动刷新页面。');
          location.reload();
        }
      }catch(e){}
      return res;
    });
  };
})();

/* ---------- 非阻塞 toast：替代正常流程里的 alert（"应用已重启需刷新"这类阻断式提示除外） ---------- */
function toast(msg, cls){
  const box = document.getElementById('toasts'); if(!box) return;
  const d = document.createElement('div'); d.className = 'toast' + (cls ? ' ' + cls : ''); d.textContent = msg;
  box.appendChild(d);
  setTimeout(()=>{ d.classList.add('out'); setTimeout(()=>d.remove(), 350); }, 4200);
  while(box.children.length > 4) box.firstChild.remove();
}
/* esc：把服务端/外部数据插值进 innerHTML 前转义（日志内容会回流进模型参数，不可信任） */
function esc(s){ return String(s == null ? '' : s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;'); }

/* ---------- 日志（按 scope 分流 + 工具条） ---------- */
// ekRefreshLogBars 按当前语言重排行数标签（切换语言时调用；日志正文保持原文）
function ekRefreshLogBars(){
  if(typeof LOG === 'undefined') return;
  for(const k in LOG){
    const L = LOG[k]; if(!L) continue;
    const bar = document.getElementById(L.bar);
    if(bar) bar.textContent = t('{0} 行', L.n || 0);
  }
  ekSyncLogToggle();
}

/* 折叠按钮的文案必须由**当前状态**推导，不能照抄标记层：
   日志默认是 collapsed，按钮却写死「折叠」，用户看到的就是
   "日志已经收起 + 按钮写着折叠"，点一次反而展开——要点两次才顺。
   文案统一走 t()，否则英文界面里点一下会突然变回中文。 */
function ekSyncLogToggle(){
  if(typeof LOG === 'undefined') return;
  document.querySelectorAll('[data-log][data-act="toggle"]').forEach(b=>{
    const L = LOG[b.dataset.log]; if(!L) return;
    const w = document.getElementById(L.wrap); if(!w) return;
    b.textContent = w.classList.contains('collapsed') ? t('展开') : t('折叠');
  });
}

const LOG = {
  install:{el:$('#log-install'), bar:'lm-install', dot:'ld-install', wrap:'lw-install', n:0, auto:true, light:false, lines:[]},
  config:{el:$('#log-config'), bar:'lm-config', dot:'ld-config', wrap:'lw-config', n:0, auto:true, light:false, lines:[]},
  web:{el:$('#log-web'), bar:'lm-web', dot:'ld-web', wrap:'lw-web', n:0, auto:true, light:false, lines:[]},
  backend:{el:$('#log-backend'), bar:'lm-backend', dot:'ld-backend', wrap:'lw-backend', n:0, auto:true, light:false, lines:[]},
  chain:{el:$('#log-chain'), bar:'lm-chain', dot:'ld-chain', wrap:'lw-chain', n:0, auto:true, light:false, lines:[]},
  // 评测日志：后端 info(scEval,...) 直接按 scope=eval 流进来，不需要单独的轮询接口
  eval:{el:$('#log-eval'), bar:'lm-eval', dot:'ld-eval', wrap:'lw-eval', n:0, auto:true, light:false, lines:[]}
};
function appendLog(scope, src, line){
  let keys;
  if(scope==='sys') keys = ['install','config','chain','web','backend'];
  else if(scope==='start') keys = src ? [src] : ['web','backend'];
  else keys = [scope];
  keys.forEach(k=>{
    const L = LOG[k]; if(!L) return;
    const div = document.createElement('div'); div.className='ln';
    const m = line.match(/\[(\w+)\]/);
    if(m){ const lvl=m[1];
      if(['FAIL','ERROR'].includes(lvl)) div.classList.add('err');
      else if(['WARN','HINT'].includes(lvl)) div.classList.add('warn');
      else if(lvl==='OK') div.classList.add('ok');
      else if(lvl==='INFO') div.classList.add('INFO'); }
    /* 无日志级别标签的行：按内容兜底识别错误/警告（新手看不懂日志，先让错误自己"跳出来"） */
    if(!m){
      if(/(fatal|panic|失败|错误|无法|不存在|拒绝|refused|denied|EADDRINUSE|exit status|cannot)/i.test(line)) div.classList.add('err');
      else if(/(警告|warn|hint|重试)/i.test(line)) div.classList.add('warn');
    }
    div.textContent = line;
    L.el.appendChild(div); L.n++; L.lines.push(line);
    const bar = document.getElementById(L.bar); if(bar) bar.textContent = t('{0} 行', L.n);
    const dot = document.getElementById(L.dot);
    if(dot){ dot.className='dot'; if(lvlOf(line)==='err') dot.classList.add('err'); else if(lvlOf(line)==='ok') dot.classList.add('ok'); }
    if(L.auto) L.el.scrollTop = L.el.scrollHeight;
  });
}
function lvlOf(line){ const m=line.match(/\[(\w+)\]/); if(!m) return ''; const l=m[1];
  if(['FAIL','ERROR'].includes(l)) return 'err'; if(['WARN','HINT'].includes(l)) return 'warn'; if(l==='OK') return 'ok'; return ''; }
const es = new EventSource('/api/events?t=' + encodeURIComponent(window.__EK_TOKEN__));
es.onmessage = e => { try{ const m=JSON.parse(e.data); appendLog(m.scope, m.src, m.line); }catch(_){} };
let esErrs = 0;
es.onopen = ()=>{ esErrs = 0; };   // 重连成功即复位计数，避免累计误杀
es.onerror = () => { if(++esErrs >= 6){ es.close(); alert('与 EnvKit 的连接已断开（可能已重启），点击确定刷新页面。'); location.reload(); } };

document.addEventListener('click', ev=>{
  const b = ev.target.closest('[data-log]'); if(!b) return;
  const k=b.dataset.log, act=b.dataset.act, L=LOG[k];
  if(act==='toggle'){ const w=document.getElementById(L.wrap); w.classList.toggle('collapsed'); b.textContent = w.classList.contains('collapsed') ? t('展开') : t('折叠'); }
  else if(act==='clear'){ L.el.innerHTML=''; L.n=0; L.lines=[]; document.getElementById(L.bar).textContent=t('{0} 行', 0); }
  else if(act==='copy'){ navigator.clipboard.writeText(L.lines.join('\n')); b.textContent=t('已复制'); setTimeout(()=>b.textContent=t('复制'),1200); }
  else if(act==='download'){ const blob=new Blob([L.lines.join('\n')],{type:'text/plain'}); const a=document.createElement('a'); a.href=URL.createObjectURL(blob); a.download='envkit-'+k+'.log'; a.click(); }
  else if(act==='light'){ L.light=!L.light; L.el.classList.toggle('light', L.light); b.classList.toggle('on', L.light); }
  else if(act==='auto'){ L.auto=!L.auto; b.classList.toggle('on', L.auto); }
});

/* ---------- 路由 ---------- */
function goto(panel){
  document.querySelectorAll('.nav').forEach(x=>x.classList.toggle('active', x.dataset.panel===panel));
  document.querySelectorAll('.panel').forEach(p=>p.classList.toggle('show', p.id==='panel-'+panel));
  // 切进来才拉，避免每次启动都读一遍文件（v2.7：审计面板同时拉看板统计）
  if(panel==='audit'){ loadAudit(); loadStats(); }
  // 设置页每次进入都重读：它和「程序配置」「链端校验」共用同一份 config.json，
  // 缓存一份只在打开时读的值，切回来就会显示过期的目录/端口。
  if(panel==='settings'){ ekSettingsEnter(); }
}
document.querySelectorAll('.nav').forEach(n=>n.addEventListener('click',()=>goto(n.dataset.panel)));
document.querySelectorAll('[data-goto]').forEach(c=>c.addEventListener('click',()=>goto(c.dataset.goto)));
// 深链：#config / #chain / #audit … 托盘右键「程序配置」直接落到对应面板。
// 用 goto 且不模拟点击：新标签页打开时也能一次到位。
function ekApplyHash(){
  const h = (location.hash || '').replace(/^#/, '');
  if(h && document.getElementById('panel-' + h)) goto(h);
}
ekApplyHash();
window.addEventListener('hashchange', ekApplyHash);
$('#next-install').onclick = ()=>goto('config');
$('#next-config').onclick = ()=>goto('chain');
$('#next-chain').onclick = ()=>goto('start');

/* ---------- 配置 + 自动保存 ---------- */
function syncStartDirs(){
  $('#fe_dir2').value = (cfg.projects&&cfg.projects.frontend_dir)||'';
  $('#be_dir2').value = (cfg.projects&&cfg.projects.backend_dir)||'';
}
function projectsBody(){
  return {
    frontend_dir:$('#fe_dir').value.trim(), backend_dir:$('#be_dir').value.trim(),
    mysql_host:$('#mysql_host').value.trim(), mysql_port:parseInt($('#mysql_port2').value)||3306,
    mysql_user:$('#mysql_user').value.trim(), mysql_password:$('#mysql_pass').value,
    db_name:$('#db_name').value.trim(), sql_file:$('#sql_path').value.trim(),
    scan_ports:(cfg.projects&&cfg.projects.scan_ports)||[],
    web_scripts:(cfg.projects&&cfg.projects.web_scripts)||[],
    // v2.1 启动方式覆盖项：留空 = 服务端按 package.json / 项目结构推断。
    // 必须显式回传，漏了会被 mergeFormConfig 抹掉（表单提交是整体替换 Projects）。
    web_script:($('#cfg_web_script')||{}).value ? $('#cfg_web_script').value.trim() : '',
    backend_file:($('#cfg_backend_file')||{}).value ? $('#cfg_backend_file').value.trim() : ''
  };
}
function chainBody(){
  return {
    ssh_host:$('#ch_ssh_host').value.trim(), ssh_port:parseInt($('#ch_ssh_port').value)||22,
    ssh_user:$('#ch_ssh_user').value.trim(), ssh_password:$('#ch_ssh_pass').value, ssh_key:$('#ch_ssh_key').value.trim(),
    chain_host:$('#ch_host').value.trim(), chain_port:parseInt($('#ch_port').value)||20200,
    chain_dir:$('#ch_dir').value.trim(), chain_start:$('#ch_start').value.trim(),
    webase_dir:$('#ch_wdir').value.trim(), webase_start:$('#ch_wstart').value.trim(),
    webase_port:parseInt($('#ch_wport').value)||5002, group_id:parseInt($('#ch_group').value)||1,
    chain_autorecover:$('#ch_autorecover').checked,
    chain_guard:$('#ch_guard').checked,
    chain_guard_secs:parseInt($('#ch_guard_secs').value)||60,
    chain_notify:$('#ch_notify').checked
  };
}
function configBody(){
  const comps = (cfg.components||[]).map((c,i)=>({...c, url:(document.getElementById('compurl-'+i)||{}).value||c.url}));
  return {
    app_name:cfg.app_name, app_title:cfg.app_title,
    install_dir:$('#install_dir').value,
    proxy:{http:$('#proxy_http').value, https:$('#proxy_https').value},
    goproxy:$('#goproxy').value, gosumdb:cfg.gosumdb||'', go_cgo:cfg.go_cgo||'', npm_registry:$('#npm_registry').value,
    mysql:{password:$('#mysql_password').value, port:parseInt($('#mysql_port').value)||3306, service_name:$('#mysql_service').value},
    projects:projectsBody(), chain:chainBody(), components:comps,
    /* ai 由「AI 助手」页单独保存（/api/ai/config）：这里必须原样回传，
       否则程序配置页的自动保存会把 API Key 抹掉（后端也有兜底合并） */
    ai:cfg.ai
    /* pet（桌宠天气）**不回传**：它同样由独立接口 /api/pet/config 保存，
       且表单页没有对应控件。后端 mergeFormConfig 会无条件保留 cur.Pet，
       两侧都别改 —— 少了任何一侧，改一下"安装目录"就会把天气开关抹成关闭 */
  };
}
function markDirty(){
  if(!loaded) return;
  dirty = true;
  $('#save-state').textContent = '未保存…';
  $('#save-state2').textContent = '未保存…';
  $('#save-state3').textContent = '未保存…';
  clearTimeout(saveTimer);
  saveTimer = setTimeout(saveNow, 800);
}
async function saveNow(){
  if(!loaded || !dirty) return;
  const body = configBody();
  const fail = (msg)=>{ ['save-state','save-state2','save-state3'].forEach(id=>{ const el=$('#'+id); el.textContent=msg; el.title=msg; }); };
  try{
    const r = await fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    if(r.ok){ cfg = body; dirty = false; $('#save-state').textContent=t('已保存'); $('#save-state2').textContent=t('已保存'); $('#save-state3').textContent=t('已保存'); syncStartDirs(); loadLaunchPlan(); }
    else if(r.status===400){
      // 硬校验拒绝：把第一个问题亮在保存状态上，完整列表放悬浮提示
      let msg = t('保存失败');
      try{ const j = await r.json(); if(j.problems && j.problems.length){ msg = t('保存失败：{0}', j.problems[0].msg); const all = j.problems.map(p=>p.field+' → '+p.msg).join('\n'); fail(msg); setTimeout(()=>{ ['save-state','save-state2','save-state3'].forEach(id=>{ $('#'+id).title=all; }); },0); return; } }catch(e){}
      fail(msg);
    }
    else { fail(t('保存失败')); }
  }catch(e){ fail(t('保存失败')); }
}
/* 有未保存改动时关页前提醒（自动保存有 800ms 窗口期，期间关页会静默丢失） */
window.addEventListener('beforeunload', e=>{
  if(dirty){ e.preventDefault(); e.returnValue = ''; }
});
async function loadConfig(){
  cfg = await (await fetch('/api/config')).json();
  $('#title').textContent = cfg.app_name || 'EnvKit';
  $('#subtitle').textContent = cfg.app_title || '统一开发环境助手';
  $('#install_dir').value = cfg.install_dir||'';
  $('#proxy_http').value = (cfg.proxy&&cfg.proxy.http)||'';
  $('#proxy_https').value = (cfg.proxy&&cfg.proxy.https)||'';
  $('#goproxy').value = cfg.goproxy||'';
  $('#npm_registry').value = cfg.npm_registry||'';
  $('#mysql_password').value = cfg.mysql?.password||'';
  $('#mysql_port').value = cfg.mysql?.port||'';
  $('#mysql_service').value = cfg.mysql?.service_name||'';
  $('#fe_dir').value = cfg.projects?.frontend_dir||'';
  $('#be_dir').value = cfg.projects?.backend_dir||'';
  // v2.1 启动方式覆盖项回填
  if($('#cfg_web_script')) $('#cfg_web_script').value = cfg.projects?.web_script||'';
  if($('#cfg_backend_file')) $('#cfg_backend_file').value = cfg.projects?.backend_file||'';
  $('#mysql_host').value = cfg.projects?.mysql_host||'';
  $('#mysql_port2').value = cfg.projects?.mysql_port||'';
  $('#mysql_user').value = cfg.projects?.mysql_user||'';
  $('#mysql_pass').value = cfg.projects?.mysql_password||'';
  $('#db_name').value = cfg.projects?.db_name||'';
  $('#sql_path').value = cfg.projects?.sql_file||'';
  const C = cfg.chain||{};
  $('#ch_ssh_host').value=C.ssh_host||''; $('#ch_ssh_port').value=C.ssh_port||22;
  $('#ch_ssh_user').value=C.ssh_user||''; $('#ch_ssh_pass').value=C.ssh_password||''; $('#ch_ssh_key').value=C.ssh_key||'';
  $('#ch_host').value=C.chain_host||''; $('#ch_port').value=C.chain_port||20200;
  $('#ch_dir').value=C.chain_dir||''; $('#ch_start').value=C.chain_start||'';
  $('#ch_wdir').value=C.webase_dir||''; $('#ch_wstart').value=C.webase_start||'';
  $('#ch_wport').value=C.webase_port||5002; $('#ch_group').value=C.group_id||1;
  $('#ch_autorecover').checked = !!C.chain_autorecover;
  $('#ch_guard').checked = !!C.chain_guard;
  $('#ch_guard_secs').value = C.chain_guard_secs||60;
  $('#ch_notify').checked = C.chain_notify!==false;
  $('#cp-20200').textContent = C.chain_port||20200;
  $('#cp-5002').textContent = C.webase_port||5002;
  syncStartDirs();
  psRenderChips(); wlRender();
  loadGuardStatus();
  // 启动页的脚本选择器：留空 = 由服务端按 package.json 推断（v2.1）。
  // 旧版本在这里硬填 'serve'，导致"项目没有 serve 脚本"时必然失败，
  // 而且这个值只存在 localStorage 里，AI 读不到。
  try{ $('#web_script').value = localStorage.getItem('ek-web-script') || ''; }catch(_){ $('#web_script').value = ''; }
  const box = $('#comp-urls'); box.innerHTML='';
  (cfg.components||[]).forEach((c,i)=>{
    const h=document.createElement('div'); h.style.margin='6px 0';
    const lb=document.createElement('label'); lb.textContent = (c.name||'') + ' ' + (c.version||''); h.appendChild(lb);
    const inp=document.createElement('input'); inp.value=c.url||''; inp.id='compurl-'+i; inp.dataset.cfg='1';
    h.appendChild(inp); box.appendChild(h);
  });
  document.querySelectorAll('[data-cfg]').forEach(el=>{
    if(el.dataset.bound) return; el.dataset.bound='1';
    el.addEventListener('input', markDirty);
    el.addEventListener('change', ()=>{ if(el.id==='be_dir') detectSQL(); });
  });
  loaded = true;
  refreshGuards();
  detectSQL();
  loadLaunchPlan();
}

/* ---------- 内置浏览器 ---------- */
let bm = { mode:'dir', path:'', ext:'', onPick:null, native:'dir' };
function winJoin(p,n){ return p.replace(/[\\\/]+$/,'') + '\\' + n; }
function openBrowser(mode, initial, ext, nativeKind, onPick, remote){
  bm = { mode, path:'', ext:ext||'', onPick, native:nativeKind, remote:!!remote };
  $('#bm-title').textContent = (remote? '远程目录（Linux · 经 SSH）：' : '') + (mode==='file' ? '选择文件' : '选择文件夹');
  $('#bm-pick').style.display = mode==='file' ? 'none' : '';
  $('#bm-native').style.display = nativeKind ? '' : 'none';
  $('#browser').classList.add('show');
  loadLS(initial||'');
}
function joinPath(p,n){ return bm.remote ? (p.replace(/\/+$/,'') + '/' + n) : winJoin(p,n); }
function closeBrowser(){ $('#browser').classList.remove('show'); }
function bmRow(icon,label,fn){ const d=document.createElement('div'); d.className='mrow';
  const ic=document.createElement('span'); ic.textContent=icon; ic.style.opacity='.7';
  d.appendChild(ic); d.appendChild(document.createTextNode(label)); d.onclick=fn; return d; }
function emptyRow(t){ const d=document.createElement('div'); d.className='mempty'; d.textContent=t; return d; }
async function loadLS(path){
  let d;
  try{
    if(bm.remote){
      d = await (await fetch('/api/chain/ls',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({path:path||''})})).json();
    } else {
      const q = new URLSearchParams({path:path||'', mode:bm.mode, ext:bm.ext||''});
      d = await (await fetch('/api/ls?'+q)).json();
    }
  }catch(e){ return; }
  bm.path = d.path||'';
  $('#bm-path').value = d.path || (bm.remote ? '远程主机 · 选择根目录' : '此电脑');
  const list=$('#bm-list'); list.innerHTML='';
  if(!d.path){
    (d.drives||[]).forEach(dr=>list.appendChild(bmRow('■', dr, ()=>loadLS(dr))));
    if(d.error) list.appendChild(emptyRow(d.error));
    return;
  }
  const upTarget = (d.parent && d.parent !== d.path) ? d.parent : '';
  list.appendChild(bmRow('↑', t('（上一级）'), ()=>loadLS(upTarget)));
  (d.dirs||[]).forEach(n=>list.appendChild(bmRow('▸', n, ()=>loadLS(joinPath(d.path,n)))));
  (d.files||[]).forEach(n=>list.appendChild(bmRow('·', n, ()=>{ bm.onPick && bm.onPick(joinPath(d.path,n)); closeBrowser(); })));
  if(d.error) list.appendChild(emptyRow(d.error));
  if(!(d.dirs||[]).length && !(d.files||[]).length && !d.error) list.appendChild(emptyRow('空目录'));
}
function pickRemoteDir(inputId){
  const cur = $('#'+inputId).value.trim();
  openBrowser('dir', cur, '', '', p=>{ $('#'+inputId).value = p; markDirty(); refreshGuards(); }, true);
}
$('#bm-up').onclick = ()=>loadLS(bm.path ? bm.path : '');
$('#bm-cancel').onclick = closeBrowser;
$('#bm-pick').onclick = ()=>{ if(bm.path && bm.onPick) bm.onPick(bm.path); closeBrowser(); };
$('#bm-native').onclick = async ()=>{
  const url = bm.mode==='file' ? '/api/pick-file' : '/api/pick-dir';
  const r = await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({initial:bm.path||'', filter:bm.mode==='file'?'SQL 文件 (*.sql)|*.sql|所有文件 (*.*)|*.*':''})});
  const d = await r.json();
  if(d.path){ bm.onPick && bm.onPick(d.path); closeBrowser(); }
};
document.addEventListener('keydown', e=>{ if(e.key==='Escape'){ closeBrowser(); closeConfirm(false); $('#about').classList.remove('show'); } });

/* ---------- 二次确认 ---------- */
let cfResolveFn = null;
function askConfirm(title, msg, okText, danger, cancelText){
  $('#cf-title').textContent = title;
  $('#cf-msg').textContent = msg;
  $('#cf-ok').textContent = okText || '确定';
  $('#cf-cancel').textContent = cancelText || '取消';
  document.querySelector('#confirm .mbox').classList.toggle('danger', !!danger);
  $('#confirm').classList.add('show');
  return new Promise(res=>{ cfResolveFn = res; });
}
function closeConfirm(v){ $('#confirm').classList.remove('show'); document.querySelector('#confirm .mbox').classList.remove('danger'); if(cfResolveFn){ cfResolveFn(v); cfResolveFn = null; } }
$('#cf-ok').onclick = ()=>closeConfirm(true);
$('#cf-cancel').onclick = ()=>closeConfirm(false);

/* ---------- 关于 / 免责声明 ---------- */
$('#btn-about').onclick = ()=>$('#about').classList.add('show');
$('#about-close').onclick = ()=>$('#about').classList.remove('show');
$('#about').addEventListener('click', e=>{ if(e.target.id==='about') $('#about').classList.remove('show'); });

/* ---------- 退出程序（可选连带停服务） ---------- */
$('#btn-exit-app').onclick = ()=>$('#exit-app').classList.add('show');
$('#exit-cancel').onclick = ()=>$('#exit-app').classList.remove('show');
$('#exit-app').addEventListener('click', e=>{ if(e.target.id==='exit-app') $('#exit-app').classList.remove('show'); });
$('#exit-confirm').onclick = async ()=>{
  const stopSvc = $('#exit-stop-svc').checked;
  const btn = $('#exit-confirm');
  btn.disabled = true; btn.textContent = t('正在退出…');
  try{
    await fetch('/api/program/exit', {method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({stop_services: stopSvc})});
  }catch(e){}
  $('#exit-app').classList.remove('show');
  // 全屏告别层：服务端即将退出，页面会失联，别让用户对着报错的页面发呆
  const ov = document.createElement('div');
  ov.style.cssText = 'position:fixed;inset:0;background:rgba(8,12,18,.92);z-index:9999;display:flex;align-items:center;justify-content:center';
  const box = document.createElement('div');
  box.style.cssText = 'color:#e8eaed;font-size:14px;text-align:center;line-height:2';
  const h = document.createElement('div');
  h.style.cssText = 'font-size:15px;font-weight:700;margin-bottom:6px';
  h.textContent = t('EnvKit 已退出');
  const p = document.createElement('div');
  p.textContent = t('本页面已与服务失去连接，可以关闭此标签页。');
  box.appendChild(h); box.appendChild(p); ov.appendChild(box);
  document.body.appendChild(ov);
  setTimeout(()=>{ try{ btn.disabled = false; btn.textContent = t('退出'); }catch(e){} }, 3000);
};

/* ---------- 主题切换 ---------- */
function applyTheme(mode){   // 注意：参数不能叫 t，会遮蔽全局翻译函数 t()
  document.body.dataset.theme = mode;
  try{ localStorage.setItem('ek-theme', mode); }catch(e){}
  const b = document.getElementById('btn-theme'); if(b) b.textContent = (mode==='dark') ? t('亮色') : t('暗色');
}
(function(){ let t='light'; try{ t = localStorage.getItem('ek-theme') || 'light'; }catch(e){} applyTheme(t); })();
(function(){
  const b = document.getElementById('btn-lang');
  if(b) b.onclick = ()=> ekSetLang(EK_LANG === 'en' ? 'zh' : 'en', true);
  ekSetLang(EK_LANG, false);
})();
$('#btn-theme').onclick = ()=> applyTheme(document.body.dataset.theme==='dark' ? 'light' : 'dark');

/* ---------- 个人中心：昵称 / 头像 / 快捷设置 ----------
   存 localStorage 而不是 config.json：这是"这台浏览器上的我"，不是环境配置；
   也不进 exe 的分发包。头像上传在 canvas 里裁成 128×128，避免原图把 localStorage 撑爆。 */
let ekProfile = { name: '', avatar: '🐟' };
try{
  const p = JSON.parse(localStorage.getItem('ek-profile') || 'null');
  if(p && typeof p === 'object'){
    if(typeof p.name === 'string') ekProfile.name = p.name.slice(0, 16);
    if(typeof p.avatar === 'string' && p.avatar) ekProfile.avatar = p.avatar;
  }
}catch(_){}

function ekProfileSave(){ try{ localStorage.setItem('ek-profile', JSON.stringify(ekProfile)); }catch(_){} }

function ekProfilePaint(){
  const paint = el=>{
    if(!el) return;
    el.innerHTML = '';
    if(/^data:image\//.test(ekProfile.avatar)){
      const img = document.createElement('img');
      img.alt = ''; img.src = ekProfile.avatar; el.appendChild(img);
    } else {
      el.textContent = ekProfile.avatar || '🐟';
    }
  };
  paint(document.getElementById('profile-avatar'));
  paint(document.getElementById('profile-avatar-big'));
  const nm = document.getElementById('profile-name');
  if(nm) nm.textContent = ekProfile.name || t('我');
  // 预选头像高亮：上传自定义图时没有预设命中，全部取消高亮
  document.querySelectorAll('#profile-presets [data-avatar]').forEach(b=>{
    b.classList.toggle('on', b.dataset.avatar === ekProfile.avatar);
  });
  const inp = document.getElementById('profile-nick');
  if(inp && document.activeElement !== inp) inp.value = ekProfile.name;
}

/* 开场问候文案抽成函数：单测/CDP 可以直接读它，不必等 1.2 秒看气泡。 */
function ekGreetText(){
  const h = new Date().getHours();
  const g = t(h<5?'夜深了，注意休息':h<11?'早上好':h<14?'中午好':h<18?'下午好':'晚上好');
  const nm = (ekProfile.name || '').trim();
  return nm ? t('{0}，{1}！环境很健康', g, nm) : t('{0}，环境很健康', g);
}

(function(){
  ekProfilePaint();
  const btn = document.getElementById('btn-profile');
  const pop = document.getElementById('profile-pop');
  if(btn && pop){
    btn.onclick = e=>{ e.stopPropagation(); pop.hidden = !pop.hidden; };
    document.addEventListener('click', e=>{
      if(!pop.hidden && !pop.contains(e.target) && !btn.contains(e.target)) pop.hidden = true;
    });
    document.addEventListener('keydown', e=>{ if(e.key === 'Escape') pop.hidden = true; });
    // 工具类动作点完就收；语言/主题留在面板里（用户常连着切）
    ['btn-av','btn-avclean','btn-elevate'].forEach(id=>{
      const b = document.getElementById(id);
      if(b) b.addEventListener('click', ()=>{ pop.hidden = true; });
    });
  }
  const nick = document.getElementById('profile-nick');
  if(nick) nick.addEventListener('input', ()=>{
    ekProfile.name = nick.value.trim().slice(0, 16);
    ekProfileSave(); ekProfilePaint();
  });
  document.querySelectorAll('#profile-presets [data-avatar]').forEach(b=>{
    b.onclick = ()=>{ ekProfile.avatar = b.dataset.avatar; ekProfileSave(); ekProfilePaint(); };
  });
  const file = document.getElementById('profile-avatar-file');
  if(file) file.addEventListener('change', ()=>{
    const f = file.files && file.files[0];
    if(!f) return;
    const rd = new FileReader();
    rd.onload = ()=>{
      const img = new Image();
      img.onload = ()=>{
        const s = Math.min(img.width, img.height) || 1;
        const cv = document.createElement('canvas');
        cv.width = 128; cv.height = 128;
        const cx = cv.getContext('2d');
        cx.drawImage(img, (img.width - s) / 2, (img.height - s) / 2, s, s, 0, 0, 128, 128);
        let url = cv.toDataURL('image/png');
        if(url.length > 200000) url = cv.toDataURL('image/jpeg', 0.85);
        ekProfile.avatar = url; ekProfileSave(); ekProfilePaint();
      };
      img.onerror = ()=> toast(t('这张图读不出来，换一张试试'), 'err');
      img.src = rd.result;
    };
    rd.readAsDataURL(f);
    file.value = '';
  });
  const reset = document.getElementById('profile-avatar-reset');
  if(reset) reset.onclick = ()=>{ ekProfile.avatar = '🐟'; ekProfileSave(); ekProfilePaint(); };

  // ---------- 桌宠：天气开关（默认关闭，需用户显式打开） ----------
  // 独立接口 /api/pet/config，不走程序配置页的表单保存。
  const won  = document.getElementById('pet-weather-on');
  const wcity = document.getElementById('pet-weather-city');
  const wwrap = document.getElementById('pet-weather-city-wrap');
  /* 保存合并：开关和城市共用一个"待发补丁"，而不是各自一个定时器。
     旧实现是"清掉上一个再排新一个"——用户输完城市在几百毫秒里点了开关，
     开关那次保存就被城市的回调取消掉了：界面显示已切换，落盘的还是旧值，
     刷新后开关"自己弹回去"。现在每次推新补丁是**合并字段**，谁都不会顶掉谁。 */
  let wPending = null, wSaveTimer = null;
  function petCfgPush(patch){
    wPending = Object.assign({}, wPending, patch);
    clearTimeout(wSaveTimer);
    wSaveTimer = setTimeout(async ()=>{
      const body = wPending; wPending = null;
      try{
        const r = await fetch('/api/pet/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
        if(!r.ok) throw new Error('http ' + r.status);
      }catch(e){
        wPending = Object.assign({}, body, wPending); // 保存失败把补丁还回去，下次改动时一并重发，别静默丢
        toast(t('桌宠设置没保存成功'), 'err');
      }
    }, 400);
  }
  function petWeatherFromPanel(){
    if(!won) return;
    if(wcity){
      // 城市复选框没开时不用显示输入框，避免"填了但不生效"的困惑
      if(wwrap) wwrap.hidden = !won.checked;
      wcity.disabled = !won.checked;
    }
  }
  if(won){
    fetch('/api/pet/config').then(r=>r.json()).then(j=>{
      won.checked = !!(j && j.weather_enabled);
      if(wcity) wcity.value = (j && j.weather_city) || '';
      petWeatherOn = won.checked;
      petWeatherMenuSync();
      petWeatherFromPanel();
    }).catch(()=>{ /* 读不到就按"关"显示，用户自己打开即可 */ petWeatherFromPanel(); });
    won.addEventListener('change', ()=>{
      petWeatherOn = won.checked;
      petWeatherMenuSync(); petWeatherFromPanel();
      petCfgPush({weather_enabled: won.checked});
    });
  }
  if(wcity){
    // 不再自己套一层 600ms 定时器：petCfgPush 已经在做防抖，两层叠加会让
    // "停手到落盘"变成 1 秒；直接推给它即可。
    wcity.addEventListener('input', ()=> petCfgPush({weather_city: wcity.value}));
  }
})();


/* ---------- 内嵌 SSH 终端（xterm.js + WebSocket 桥接） ---------- */
let term=null, fitAddon=null, termWS=null, termRO=null;
function termFit(){ try{ const w=document.getElementById('term-wrap'); if(!w || !w.offsetParent) return; if(fitAddon) fitAddon.fit(); }catch(e){} }
$('#btn-term-connect').onclick = ()=>{
  if(!term){
    term = new Terminal({cursorBlink:true, fontSize:13, fontFamily:'Consolas,"Courier New",monospace',
      theme:{background:'#10161f', foreground:'#d7dde6', cursor:'#9ad1a8', selectionBackground:'#2f7d4f55'}});
    fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open(document.getElementById('term'));
    term.onData(d=>{ if(termWS && termWS.readyState===1) termWS.send(new TextEncoder().encode(d)); });
    term.onResize(({cols,rows})=>{ if(termWS && termWS.readyState===1) termWS.send(JSON.stringify({type:'resize',cols,rows})); });
    termRO = new ResizeObserver(()=>termFit());
    termRO.observe(document.getElementById('term-wrap'));
    window.addEventListener('resize', termFit);
  }
  document.getElementById('term-card').style.display='';
  termSetCollapsed(false);   // 已收起状态下点「连接终端」＝重新展开
  const host=$('#ch_ssh_host').value.trim(), user=$('#ch_ssh_user').value.trim();
  document.getElementById('term-title').textContent = (user||'root')+'@'+(host||'未配置主机')+' — ssh';
  setTimeout(termFit, 30);
  if(termWS && (termWS.readyState===0 || termWS.readyState===1)) return;
  const proto=location.protocol==='https:'?'wss':'ws';
  termWS = new WebSocket(proto+'://'+location.host+'/api/chain/term?t='+encodeURIComponent(window.__EK_TOKEN__));
  termWS.binaryType='arraybuffer';
  termWS.onopen=()=>{ term.writeln('\x1b[32m[已连接 '+(user||'root')+'@'+host+']\x1b[0m'); termFit(); term.focus(); };
  termWS.onmessage=ev=>{ term.write(ev.data instanceof ArrayBuffer ? new Uint8Array(ev.data) : ev.data); };
  termWS.onclose=()=>{ term.writeln('\x1b[33m[连接已关闭]\x1b[0m'); termWS=null; };
  termWS.onerror=()=>{ term.writeln('\x1b[31m[连接错误，请检查 SSH 配置]\x1b[0m'); };
};
function termDisconnect(){ if(termWS) termWS.close(); }
/* 「断开」只剩终端标题栏那一颗：配置/操作分离后独立按钮收进标题栏，不再单列 */
$('#btn-term-close2').onclick = ()=>{ termDisconnect(); document.getElementById('term-card').classList.remove('full'); };
$('#btn-term-clear').onclick = ()=>{ if(term) term.clear(); };
$('#btn-term-full').onclick = ()=>{ document.getElementById('term-card').classList.toggle('full'); setTimeout(termFit, 40); };

/* 收起/展开：只切 UI，不动 WebSocket——SSH 会话在后台继续，展开后无需重连 */
let termCollapsed=false;
function termSetCollapsed(c){
  termCollapsed=c;
  document.getElementById('term-card').classList.toggle('collapsed', c);
  const b=$('#btn-term-collapse'); if(b) b.textContent = c ? t('展开') : t('收起');
  if(!c) setTimeout(termFit, 40);   // 展开后重新适配尺寸（隐藏期间 ResizeObserver 被 termFit 跳过）
}
$('#btn-term-collapse').onclick = ()=> termSetCollapsed(!termCollapsed);

/* ---------- SSH 密码显示切换 ---------- */
$('#ch_pass_eye').onclick = ()=>{
  const p=document.getElementById('ch_ssh_pass');
  const show = p.type==='password';
  p.type = show ? 'text' : 'password';
  document.getElementById('ch_pass_eye').textContent = show ? '隐藏' : '显示';
};

/* ---------- 杀软白名单 ---------- */
$('#btn-av').onclick = async ()=>{
  if(await askConfirm('加入杀软白名单','将通过 UAC 申请管理员权限，把 EnvKit 所在目录和 EnvKit.exe\n加入 Windows Defender 排除项，防止 go / npm / mysqldump 子进程被误拦。\n\n弹出 UAC 窗口时请点「是」。','加入')) post('/api/av-exclude');
};
(async ()=>{ try{
  const s = await (await fetch('/api/av-exclude')).json();
  const b = document.getElementById('btn-av');
  if(s.defender===false){ b.textContent='杀软：第三方'; b.disabled=true; }
  else if(s.excluded){ b.textContent='已加白名单'; b.disabled=true; }
}catch(e){} })();

/* ---------- 深度清除：仅在检测到 WROberlay 残留时才显示按钮 ---------- */
(async ()=>{ try{
  const r = await (await fetch('/api/av-clean')).json();
  if(r && r.threat) document.getElementById('btn-avclean').style.display = '';
}catch(e){} })();
/* --------------------------------------------------------------------- */
$('#btn-avclean').onclick = async ()=>{
  const okGo = await askConfirm('深度清除流氓软件残留',
    '目标：PUA:Win32/Softcnapp 家族（WROberlay*.dll 注入型广告软件）。\n' +
    '它会向新启动的进程注入 DLL，导致 go / npm / mysqldump 随机「零输出秒退」。\n\n' +
    '将执行：\n' +
    '· 调用 Defender 清除已检测威胁\n' +
    '· 删除 C:\\Windows 下所有 WROberlay* 文件（被占用则改名 .quarantine）\n' +
    '· 清除相关 CLSID 与图标覆盖注册项\n' +
    '· 重启资源管理器以卸载已加载的 DLL\n\n' +
    '建议用「以管理员身份运行」的 EnvKit 执行，可免 UAC 弹窗。完成后重启一次电脑。',
    '开始清除');
  if(okGo){
    try{ await post('/api/av-clean'); }catch(e){}
    setTimeout(()=>{ try{ goto('start'); }catch(e){} }, 300);
  }
};

/* ---------- 环境安装：演练 + 版本管理 ---------- */
$('#btn-dryrun').onclick = ()=>post('/api/install-dryrun');
$('#btn-ver-check').onclick = loadVersions;
function vtag(txt, cls){ return `<span class="vtag ${cls}">${esc(txt)}</span>`; }
async function loadVersions(){
  const h = document.getElementById('h-ver');
  h.textContent = '查询中（探测官方最新版本，约几秒）...';
  const box = document.getElementById('ver-table');
  box.innerHTML = '';
  try{
    const list = await (await fetch('/api/component-versions')).json();
    const table = document.createElement('table'); table.className = 'vtab';
    // 表头是静态文案可以 innerHTML；数据行一律 DOM + textContent 构建，杜绝注入
    table.innerHTML = '<colgroup><col style="width:14%"><col style="width:30%"><col style="width:18%"><col style="width:18%"><col style="width:20%"></colgroup><tr><th>组件</th><th>已安装</th><th>配置目标</th><th>官方最新</th><th></th></tr>';
    for(const v of list){
      const updatable = v.latest && v.configVer && verCmpJs(v.latest, v.configVer) > 0;
      const tr = document.createElement('tr');
      const tdName = document.createElement('td'); const nb = document.createElement('b'); nb.textContent = v.name; tdName.appendChild(nb);
      const tdInst = document.createElement('td');
      if(v.installedKnown){
        tdInst.innerHTML = vtag(v.installedVer + ' · ' + (v.toolDir ? 'tools' : '系统'), v.toolDir ? '' : 'muted');
      } else {
        const sp = document.createElement('span'); sp.className = 'muted'; sp.textContent = t('未检测（先点「重新检测」）'); tdInst.appendChild(sp);
      }
      const tdCfg = document.createElement('td'); tdCfg.textContent = v.configVer || '—';
      const tdLat = document.createElement('td');
      if(v.latest){ tdLat.innerHTML = vtag(v.latest, updatable ? '' : 'muted'); }
      else { const sp = document.createElement('span'); sp.className = 'muted'; sp.textContent = t('未知'); tdLat.appendChild(sp); }
      const tdOp = document.createElement('td');
      if(updatable){
        // 不再用内联 onclick 字符串拼接（v.name/v.latest 来自外部版本探测，可被注入），改闭包传参
        const btn = document.createElement('button'); btn.className = 'mini on'; btn.textContent = t('升级');
        btn.onclick = ()=>upgradeComp(v.name, v.configVer, v.latest);
        tdOp.appendChild(btn);
      } else if(v.latest){
        const sp = document.createElement('span'); sp.className = 'muted'; sp.textContent = t('已是最新'); tdOp.appendChild(sp);
      }
      tr.appendChild(tdName); tr.appendChild(tdInst); tr.appendChild(tdCfg); tr.appendChild(tdLat); tr.appendChild(tdOp);
      table.appendChild(tr);
    }
    box.appendChild(table);
    h.textContent = '查询完成。升级完成后点「检查更新」刷新状态。';
  }catch(e){ h.textContent = t('查询失败：{0}', e); }
}
function verCmpJs(a, b){
  const pa = String(a).split('.'), pb = String(b).split('.');
  for(let i = 0; i < Math.max(pa.length, pb.length); i++){
    const x = parseInt(pa[i])||0, y = parseInt(pb[i])||0;
    if(x !== y) return x < y ? -1 : 1;
  }
  return 0;
}
async function upgradeComp(name, from, to){
  if(!(await askConfirm('升级组件', t(`将把 {0} 从 {1} 升级到 {2}：\n下载新版（数百 MB）→ 解压新目录 → 自动更新 PATH 与配置。\n旧版本目录会保留，可随时回滚。\n\n继续？`, name, from, to),'升级'))) return;
  post('/api/component-upgrade', {name});
  document.getElementById('h-ver').textContent = t('{0} 升级中，进度见下方安装日志，完成后点「检查更新」刷新。', name);
}

/* ---------- 配置导入 / 导出 ---------- */
$('#btn-cfg-export').onclick = ()=>{ location.href='/api/config/export?t=' + encodeURIComponent(window.__EK_TOKEN__); };
$('#btn-cfg-sanitize').onclick = ()=>{ location.href='/api/config/export?sanitize=1&t=' + encodeURIComponent(window.__EK_TOKEN__); };
$('#btn-cfg-import').onclick = ()=>{ $('#cfg-file').click(); };
/* 导出分发包：把当前 exe 打成 zip（exe+安装/卸载+说明）直接下载；泄漏扫描失败时后端返回 409 文本 */
$('#btn-dist-export').onclick = async ()=>{
  try{
    const resp = await fetch('/api/dist/export?t=' + encodeURIComponent(window.__EK_TOKEN__));
    if(!resp.ok){
      const tx = await resp.text();
      alert(t('导出失败：') + tx);
      return;
    }
    const blob = await resp.blob();
    const m = (resp.headers.get('Content-Disposition')||'').match(/filename=([^;]+)/);
    const a=document.createElement('a');
    a.href=URL.createObjectURL(blob);
    a.download = m ? m[1] : 'EnvKit-dist.zip';
    a.click();
    setTimeout(()=>URL.revokeObjectURL(a.href), 30000);
  }catch(e){ alert(t('导出失败：') + e); }
};

/* ---------- 数据库备份 / 还原 ---------- */
$('#btn-db-backup').onclick = ()=>post('/api/program/db-backup');
// 恢复演练：备份 → 恢复到 __drill_ 临时库 → 比对表数 → 删临时库，全程不碰业务库
$('#btn-db-drill').onclick = ()=>post('/api/program/db-drill');
$('#btn-db-restore').onclick = async ()=>{
  const f=$('#restore_path').value.trim();
  if(!f){ alert('请先选择备份 .sql 文件'); return; }
  const db=$('#db_name').value.trim();
  await postConfirm('/api/program/db-restore', {file:f}, t('还原数据库'),
    t('将用备份文件覆盖数据库 {0}：\n{1}\n\n当前库中的同表数据将被替换，且无法撤销！', db, f));
};
function pickRestore(){ openBrowser('file', (cfg.projects&&cfg.projects.backend_dir)||'', '.sql', 'file', p=>{ $('#restore_path').value=p; }); }
$('#btn-pick-restore').onclick = pickRestore;

/* ---------- 环境诊断报告 ---------- */
$('#btn-report').onclick = ()=>{ location.href = '/api/report?t=' + encodeURIComponent(window.__EK_TOKEN__); };
$('#cfg-file').onchange = async e=>{
  const f=e.target.files[0]; if(!f) return;
  try{
    const text=await f.text();
    JSON.parse(text); // 先本地校验，避免把坏文件发给后端
    const r=await fetch('/api/config/import',{method:'POST',body:text});
    if(!r.ok){
      // 400 时后端返回 {ok:false, problems:[...]}，把问题逐条亮出来
      try{ const j=await r.json(); if(j.problems&&j.problems.length) throw new Error(j.problems.map(p=>p.field+' → '+p.msg).join('\n')); }catch(e2){ if(e2.message&&e2.message.includes('→')) throw e2; }
      throw new Error(await r.text());
    }
    $('#h-cfg-io').textContent='导入成功，已重新加载';
    await loadConfig();
    refreshGuards();
  }catch(err){
    $('#h-cfg-io').textContent=t('导入失败：{0}', err.message||err);
  }
  e.target.value='';
};

function pickDir(inputId, projField){
  openBrowser('dir', (cfg.projects&&cfg.projects[projField])||'', '', 'dir', p=>{
    if(inputId) $('#'+inputId).value = p;
    if(projField==='frontend_dir'){ $('#fe_dir').value = p; loadLaunchPlan(); }
    if(projField==='backend_dir'){ $('#be_dir').value = p; detectSQL(); loadLaunchPlan(); }
    syncStartDirs(); markDirty(); refreshGuards();
  });
}
function pickSQL(){ openBrowser('file', (cfg.projects&&cfg.projects.backend_dir)||'', '.sql', 'file', p=>{ $('#sql_path').value=p; markDirty(); refreshGuards(); }); }
document.querySelectorAll('[data-pick]').forEach(b=>b.addEventListener('click',()=>pickDir(b.dataset.pick,b.dataset.proj)));
document.querySelectorAll('[data-remote]').forEach(b=>b.addEventListener('click',()=>pickRemoteDir(b.dataset.remote)));
$('#btn-pick-sql').onclick = pickSQL;

/* 配置页 tab：只切显示，不动任何字段——隐藏页里的控件照样能被脚本读写，
   自动保存与前置校验因此完全不需要感知 tab 的存在。 */
(function(){
  const tabs = document.getElementById('cfg-tabs');
  if(!tabs) return;
  tabs.addEventListener('click', ev=>{
    const b = ev.target.closest('.cfg-tab'); if(!b) return;
    tabs.querySelectorAll('.cfg-tab').forEach(x=>x.classList.toggle('on', x === b));
    document.querySelectorAll('#panel-config .cfg-pane').forEach(p=>
      p.classList.toggle('show', p.dataset.cfgPane === b.dataset.cfgTab));
  });
})();

/* 自动探测后端目录下的 dbs\*.sql */
// v2.3 N4：拉取链端守护的运行状态。
// 开关只说明"你要求了它"，这里说明"它实际在做什么"——两者可能不一致
// （比如刚勾上还没到下一轮，或配置没保存）。
async function loadGuardStatus(){
  const box = $('#guard-box');
  if(!box) return;
  const badge = $('#guard-badge'), note = $('#guard-note'), recent = $('#guard-recent');
  let d = null;
  try{ d = await (await fetch('/api/guard/status')).json(); }catch(e){}
  const s = (d && d.status) || null;
  if(!s){ badge.textContent = t('读取失败'); note.textContent = ''; recent.innerHTML=''; return; }

  // 徽标分三色：进行中=绿、未开启=灰、有问题=橙。
  // **连续不可达单独一档**：它需要用户去处理（虚拟机没开/IP 变了），
  // 不是"守护坏了"，也不能算正常。
  let cls = 'var(--muted)', label = t('未开启');
  if(!s.host){ cls = 'var(--muted)'; label = t('未配置'); }
  else if(s.last_unreachable > 0){ cls = 'var(--warn)'; label = t('连不上'); }
  else if(s.enabled && s.last_check){ cls = 'var(--ok)'; label = t('运行中'); }
  else if(s.enabled){ cls = 'var(--muted)'; label = t('已开启'); }
  badge.textContent = label;
  badge.style.color = cls;
  badge.style.background = 'var(--card)';

  note.textContent = s.note || '';
  // 最近动作：这才是"它到底做了什么"的答案
  const acts = (d && d.recent) || [];
  if(acts.length){
    let h = '<div class="muted" style="font-size:11px;margin-bottom:4px">' + esc(t('最近动作')) + '</div>';
    for(const a of acts){
      const okk = a.result === 'ok' || a.result === 'started';
      const mark = a.result === 'fail' ? '<span style="color:var(--err)">✕</span>' : (okk ? '<span style="color:var(--ok)">✓</span>' : '·');
      const when = String(a.ts||'').slice(5,16);
      h += '<div class="ai-plan-row" style="font-size:12px;padding:1px 0">' +
           '<span class="ai-plan-n">' + esc(when) + '</span>' +
           '<span>' + mark + ' ' + esc(a.action||'') +
           (a.detail ? ' <span class="muted">' + esc(String(a.detail).slice(0,80)) + '</span>' : '') +
           (a.verify ? ' <span class="muted">' + esc(String(a.verify).slice(0,80)) + '</span>' : '') +
           '</span></div>';
    }
    recent.innerHTML = h;
  } else {
    recent.innerHTML = '<div class="muted" style="font-size:11px">' + esc(t('暂无守护动作记录')) + '</div>';
  }
}

async function detectSQL(){
  if(!loaded) return;
  const bd = $('#be_dir').value.trim();
  if(!bd || $('#sql_path').value.trim()) return;
  try{
    const d1 = await (await fetch('/api/ls?path='+encodeURIComponent(bd))).json();
    if((d1.dirs||[]).some(x=>x.toLowerCase()==='dbs')){
      const dbs = winJoin(bd,'dbs');
      const d2 = await (await fetch('/api/ls?path='+encodeURIComponent(dbs)+'&mode=file&ext=.sql')).json();
      if((d2.files||[]).length){ $('#sql_path').value = winJoin(dbs, d2.files[0]); markDirty(); refreshGuards(); }
    }
  }catch(e){}
}

/* ---------- 前置校验 ---------- */
function refreshGuards(){
  const fe=$('#fe_dir').value.trim(), be=$('#be_dir').value.trim(), db=$('#db_name').value.trim(), sql=$('#sql_path').value.trim();
  const set=(btnId,hintId,ok,hint)=>{ const b=$('#'+btnId); if(b) b.disabled=(!ok)||!!busyNow; const h=$('#'+hintId); if(h) h.textContent=ok?'':hint; };
  set('btn-init-db','h-init-db', !!db, '缺少：数据库名');
  if(sql==='') $('#h-init-db').textContent = db? '未选 SQL：将只建库' : '缺少：数据库名';
  set('btn-apply-sql-only','h-init-db', !!sql, '');
  set('btn-web-install','h-deps', !!fe, '缺少：前端目录');
  set('btn-backend-tidy','h-deps', !!be, '缺少：后端目录');
  if(fe || be) $('#h-deps').textContent = '';
  set('btn-web-start','h-web-start', !!fe, '缺少：前端目录');
  set('btn-backend-start','h-backend-start', !!be, '缺少：后端目录');
  $('#dep-state').textContent = t('前端 {0} · 后端 {1}', fe?t('已配置'):'—', be?t('已配置'):'—');
  const cd=$('#ch_dir').value.trim(), cst=$('#ch_start').value.trim(), wd=$('#ch_wdir').value.trim(), wst=$('#ch_wstart').value.trim();
  set('btn-start-chain','h-chain', !!(cd&&cst), '需先填链端目录与启动脚本');
  set('btn-start-webase','h-webase', !!(wd&&wst), '需先填 WeBASE 目录与启动脚本');
  ['btn-check','btn-reinstall','btn-db-test','btn-ssh-test','btn-ssh-exec','btn-chain-check','btn-ssh-term'].forEach(id=>{ const b=document.getElementById(id); if(b) b.disabled=!!busyNow; });
}

/* ---------- 检测 / 安装 ---------- */
function cardHTML(r){
  const yes=r.installed;
  const ver=r.version?`<div class="kv">${t('版本：')}${esc(r.version)}</div>`:'';
  const loc=r.location?`<div class="kv">${t('位置：')}${esc(r.location)}</div>`:(yes?'':'<div class="kv">' + t('未检测到') + '</div>');
  /* 失败项直接给"一键安装"：安装流程幂等，已装的会跳过，只补缺的 */
  const fix = yes ? '' : `<div style="margin-top:6px"><button class="mini" data-fix="1">${t('一键安装')}</button></div>`;
  return `<div class="comp"><div class="name">${esc(r.name)}<span class="badge ${yes?'yes':'no'}">${yes?t('已安装'):t('缺失')}</span></div>${ver}${loc}${fix}</div>`;
}
/* 检测卡片"一键安装"：事件委托，两个卡片容器都生效 */
document.addEventListener('click', async ev=>{
  const b = ev.target.closest('[data-fix]'); if(!b) return;
  try{
    const r = await fetch('/api/install',{method:'POST'});
    if(r.ok) toast(t('开始安装缺失组件，进度看下方安装日志'));
    else toast(t('有任务正在执行，请稍候'), 'err');
  }catch(e){ toast(t('请求失败：') + e, 'err'); }
});
function renderResults(rs){
  const html=(rs||[]).map(cardHTML).join('');
  $('#detect-cards').innerHTML = html || '<div class="muted">尚未检测</div>';
  $('#install-cards').innerHTML = html || '<div class="muted">尚未检测</div>';
}
let prevPhase = null;   // 检测 phase 迁移判定（桌宠差分表情用）
async function pollState(){
  try{
    const s = await (await fetch('/api/state')).json();
    renderResults(s.results);
    const ph=s.phase;
    /* checking→checked：检测完成，桌宠按结果做差分表情；→checking：检测开始，进忙碌态 */
    if(prevPhase==='checking' && ph==='checked'){
      const nf=(s.results||[]).filter(x=>!x.installed);
      try{ petCheckDone(nf.length, nf.map(x=>x.name).join('、')); }catch(e){}
    } else if(prevPhase!=='checking' && ph==='checking'){
      try{ petCheckStart(); }catch(e){}
    }
    prevPhase = ph;
    const d=$('#pg-detect'), c=$('#pg-config'), i=$('#pg-install');
    d.className='pseg'+(ph!=='idle'?' done':''); c.className='pseg'+(ph!=='idle'?' done':''); i.className='pseg'+(ph==='installing'?' run':(ph==='done'?' done':''));
    const ib=$('#install-banner');
    if(ph==='done'){
      const fails=(s.results||[]).filter(x=>!x.installed).length;
      if(fails>0){ ib.className='banner err'; ib.textContent=t('仍有 {0} 个组件未就绪', fails); }
      else { ib.className='banner ok'; ib.textContent='全部就绪，请重开终端使环境变量生效'; }
    }
    $('#btn-install').disabled = !!busyNow || (ph==='checking'||ph==='installing');
  }catch(e){}
}
$('#btn-check').onclick = ()=>fetch('/api/check',{method:'POST'});
$('#btn-install').onclick = ()=>fetch('/api/install',{method:'POST'});
$('#btn-reinstall').onclick = async ()=>{ if(await askConfirm('重新安装','将重新走一遍安装流程：重新下载并解压所有组件（已装的会跳过）。\n可能耗时较久。','开始重装')) fetch('/api/install',{method:'POST'}); };

/* ---------- 健康状态 ---------- */
function chip(id, cls, text){ const el=$('#'+id); el.className='chip '+cls; el.textContent=text; }
/* ---------- 操作审计：把审计流从"只有文件"变成页面上能看 ---------- */
const AUDIT_ACTOR = {user:'用户', ai:'AI', guard:'守护', system:'系统', shadow:'影子'};
let auditActor = '';
let auditDays = [];

function auditFillDays(days){
  if(!days || !days.length) return;
  const same = days.length === auditDays.length && days.every((d,i)=>d===auditDays[i]);
  auditDays = days;
  const sel = $('#audit-day'); if(!sel) return;
  if(same && sel.options.length) return;               // 没变化就别重建，否则会丢当前选择
  const cur = sel.value;
  sel.innerHTML = '';
  const o0 = document.createElement('option'); o0.value='all'; o0.textContent=t('全部日期'); sel.appendChild(o0);
  for(const d of days){
    const o = document.createElement('option'); o.value=d;
    o.textContent = d.slice(0,4)+'-'+d.slice(4,6)+'-'+d.slice(6,8);
    sel.appendChild(o);
  }
  sel.value = (cur && (cur==='all' || days.indexOf(cur)>=0)) ? cur : 'all';
}

async function loadAudit(){
  const tb = $('#audit-body'); if(!tb) return;
  const day = ($('#audit-day') && $('#audit-day').value) || 'all';
  let q = '/api/audit?n=500&day=' + encodeURIComponent(day) + (auditActor ? ('&actor='+auditActor) : '');
  let d = null;
  try{ d = await (await fetch(q)).json(); }
  catch(e){ tb.innerHTML = '<tr><td colspan="6" class="muted" style="padding:14px;text-align:center">'+esc(t('读取失败'))+'</td></tr>'; return; }
  auditFillDays(d && d.days);
  const rows = (d && d.entries) || [];
  tb.innerHTML = '';
  const h = $('#h-audit'); if(h) h.textContent = t('{0} 条', rows.length);
  if(!rows.length){
    tb.innerHTML = '<tr><td colspan="6" class="muted" style="padding:14px;text-align:center">'+esc(t('暂无审计记录'))+'</td></tr>';
    return;
  }
  for(const e of rows){
    const tr = document.createElement('tr');
    const rk = (e.result==='ok') ? 'ok' : ((e.result==='fail'||e.result==='denied') ? 'err' : 'warn');
    const col = rk==='ok' ? 'var(--ok)' : (rk==='err' ? 'var(--err)' : 'var(--warn)');
    let extra = '';
    if(e.dur_ms) extra += t('耗时 {0}ms', e.dur_ms);
    if(e.verify) extra += (extra ? '　' : '') + t('复验：{0}', e.verify);
    tr.innerHTML =
      '<td class="mini">'+esc(e.ts||'')+'</td>'+
      '<td>'+esc(AUDIT_ACTOR[e.actor] || e.actor || '')+'</td>'+
      '<td>'+esc(e.action||'')+'</td>'+
      '<td>'+esc(e.target||'')+'</td>'+
      '<td style="color:'+col+';font-weight:600">'+esc(e.result||'')+'</td>'+
      '<td>'+esc(((e.detail||'') + (extra ? ('　'+extra) : '')).trim())+'</td>';
    tb.appendChild(tr);
  }
}

function auditBind(){
  const map = {'audit-f-all':'', 'audit-f-user':'user', 'audit-f-ai':'ai', 'audit-f-guard':'guard', 'audit-f-system':'system', 'audit-f-shadow':'shadow'};
  for(const id in map){
    const b = $('#'+id); if(!b) continue;
    b.onclick = ()=>{
      auditActor = map[id];
      document.querySelectorAll('#panel-audit .mini').forEach(x=>x.classList.remove('on'));
      b.classList.add('on');
      loadAudit();
    };
  }
  const sel = $('#audit-day'); if(sel) sel.onchange = loadAudit;
  const rf = $('#audit-refresh'); if(rf) rf.onclick = ()=>{ loadAudit(); loadStats(); };
  const sr = $('#st-refresh'); if(sr) sr.onclick = loadStats;
  const sd = $('#st-days'); if(sd) sd.onchange = loadStats;
}

/* ---------- v2.7 统计看板：纯 SVG，不引图表库 ----------
 *
 * 「单文件零依赖」是本工具的硬约束，所以不用 Chart.js 之类。
 * 数据全部来自 /api/stats（由 stats.go 从审计与轨迹实时算）。
 *
 * 三条口径纪律在渲染侧同样要守：
 *  1. **无样本显式说明**，不画成 0
 *  2. **分母不同的指标不共用一根轴**（任务结局只覆盖 14 天，token 同理）
 *  3. **denied 不算失败**，图例单独一档
 */

/* stFmt 千分位；大数缩写（12.3k）——轴标签用，tooltip 用完整值 */
function stFmt(n){
  n = Number(n)||0;
  if(Math.abs(n) >= 1000000) return (n/1000000).toFixed(1).replace(/\.0$/,'')+'M';
  if(Math.abs(n) >= 1000)    return (n/1000).toFixed(1).replace(/\.0$/,'')+'k';
  return String(n);
}
function stFull(n){ return (Number(n)||0).toLocaleString('en-US'); }

/* stBars 画一组竖条。
 * data: [{label, values:{k:v}, tip}]，keys: [{k,color,name}]
 * 用 SVG 而不是 canvas：可缩放、能加<title> 悬浮提示、样式跟随 CSS 变量。 */
function stBars(box, days, keys, opt){
  opt = opt || {};
  const el = typeof box === 'string' ? $(box) : box;
  if(!el) return;
  if(!days || !days.length){ el.innerHTML = '<div class="hint">'+esc(t('暂无数据'))+'</div>'; return; }

  const W = 100, H = 100;                 // viewBox 单位
  // padL 要够宽容纳 Y 轴语义：这里不画数值刻度（数值在 tooltip 与 KPI 里），
  // 但**必须留出左边距**，否则第一根柱子会贴边甚至被 viewBox 裁掉。
  // 用 preserveAspectRatio=none 横向拉伸时，viewBox 里的百分比是相对宽度算的，
  // padL=2 相当于实际 2% 宽——太窄。
  const padL = opt.padL==null ? 4 : opt.padL, padR = 2, padB = 5, padT = 3;
  const iw = W - padL - padR, ih = H - padT - padB;

  // 顶部留白按最大值留 12%，避免柱子顶到边
  let max = 0;
  days.forEach(d => keys.forEach(k => { const v = Number(d[k.k])||0; if(v>max) max=v; }));
  if(max <= 0){
    el.innerHTML = '<div class="hint">'+esc(t('区间内没有可统计的数据'))+'</div>';
    return;
  }
  const top = max * 1.12;
  const n = days.length;
  const slot = iw / n;
  const bw = Math.max(slot * 0.62, 0.35);

  let bars = '', grid = '';
  // 横向参考线：0 / 50% / 100%
  [0, 0.5, 1].forEach(f => {
    const y = padT + ih * (1 - f);
    grid += '<line x1="'+padL+'" y1="'+y.toFixed(2)+'" x2="'+(W-padR)+'" y2="'+y.toFixed(2)+
            '" stroke="var(--line)" stroke-width="0.3" vector-effect="non-scaling-stroke"/>';
  });

  days.forEach((d, i) => {
    const x = padL + slot * i + (slot - bw) / 2;
    let stackY = padT + ih;
    keys.forEach(k => {
      const v = Number(d[k.k])||0;
      if(v <= 0) return;
      const h = (v / top) * ih;
      stackY -= h;
      bars += '<rect x="'+x.toFixed(2)+'" y="'+stackY.toFixed(2)+'" width="'+bw.toFixed(2)+
              '" height="'+h.toFixed(2)+'" fill="'+k.color+'" rx="0.3">'+
              '<title>'+esc(d.date)+' · '+esc(t(k.name))+': '+stFull(v)+'</title></rect>';
    });
  });

  // X 轴标签：**画在 HTML 层，不放进 SVG。**
  //
  // ## 为什么不能放在 SVG 里（v2.8 修）
  //
  // 原来是 `<text>` + `preserveAspectRatio="none"` + `viewBox="0 0 100 100"`。
  // 那个组合有两个坏处：
  //
  //   1. **`preserveAspectRatio=none` 会把整个坐标系横向拉伸** ——
  //      **文字也跟着变形**（横向拉长/压扁）。
  //      用户看到的「不清楚」主要就是这个。
  //   2. `font-size:2.4` 是 viewBox 单位。宽度 400px 时实际约 `2.4 * 4 = 9.6px`，
  //      太小；而高度被压到 120px，字号在不同宽度下**还会变**。
  //
  // 标签挪到 HTML 后：字号是 CSS 像素（不随图宽变），
  // 而且**能换行/用 CSS 类**，深浅色自动跟随。
  const LAB = 13;                       // 最多几个标签
  const every = Math.max(1, Math.ceil(n / LAB));
  const showAt = new Set();
  // 从 0 开始按 every 取，最后一天必标（n<=3 时 every=1，自然全标）
  for (let i = 0; i < n; i += every) showAt.add(i);
  showAt.add(n - 1);

  // 日期显示。**这里踩过一个一直存在的真 bug**：
  //
  // 后端 `stats.go:157` 用的是 `Format("20060102")` —— **没有分隔符**，
  // 所以 `d.date` 是 `'20260908'`。
  // 而旧代码写的是 `d.date.slice(5)`（那是按 `'2006-01-02'` 算的），
  // 于是从 `'20260908'` 上slice(5) 得到 `'0908'` ——
  // **跨月时更糟**：`'20261001'.slice(5)` 得到 `'0001'`，
  // 整个 10 月看起来像「1 月」。
  //
  // 用户反馈的「横坐标不清楚」底下藏着这个：
  // 不是字体问题，**是日期本身显示错了**。
  //
  // 口径：按真实格式 `'YYYYMMDD'` 解析，跨年时补年份。
  const thisYear = days[n - 1].date.slice(0, 4);
  const fmtDay = ds => {
    if (ds.length < 8) return ds;         // 不是预期格式就原样显示，不瞎猜
    const y = ds.slice(0, 4), m = ds.slice(4, 6), d = ds.slice(6, 8);
    return y === thisYear ? m + '-' + d : y.slice(2) + '/' + m + '-' + d;
  };

  let xlab = '<div class="stXlab">';
  days.forEach((d, i) => {
    if (!showAt.has(i)) return;
    const x = padL + slot * i + slot / 2;
    // 位置用百分比，与 SVG 里算出来的 x 一致 —— SVG 是拉伸的，
    // 但 padding 与 slot 都是 viewBox 单位的**比例**，换算成 % 正好对齐。
    let pct = x / W * 100;
    // 首末标签 translateX(-50%) 会溢出容器 —— 真实修正放在插入之后
    // （用实测像素宽度，只改 transform 不重排，理由见下方注释）。
    xlab += '<span style="left:'+pct.toFixed(3)+'%">'
          + esc(fmtDay(d.date)) + '</span>';
  });
  xlab += '</div>';

  el.innerHTML = '<svg viewBox="0 0 '+W+' '+H+'" preserveAspectRatio="none" '+
    'style="width:100%;height:'+(opt.height||120)+'px;display:block" role="img">'+
    grid + bars + '</svg>' + xlab;

  // ## 贴边修正：用实测像素宽度，**不重排**
  //
  // ## 为什么不能在渲染前算
  //
  // 标签宽是**像素**（约 17.8px），而位置是**百分比**。
  // 换算要知道容器宽度 —— 此刻DOM 还没插入，拿不到。
  //
  // 第一版拍了个「夹 3%」，CDP 实测 **overflowL = 17.3px**：
  // 3% 约 18px，一半 9px < 需要的 8.9px… 边缘上，
  // 而且**容器宽度会随窗口变**，拍死的百分比必然在某个宽度下失效。
  //
  // 所以插入后量一次真实边界，只改 `transform` ——
  // **不重写 `left`，因为那会触发二次布局、也可能与下次渲染不一致**。
  const labEl = el.querySelector('.stXlab');
  if (labEl && labEl.children.length) {
    const cb = labEl.getBoundingClientRect();
    const spans = labEl.querySelectorAll('span');
    const first = spans[0], last = spans[spans.length - 1];
    if (cb.width > 0 && first && last) {
      // **必须用 calc 保住 -50%**。
      //
      // 直接写 `transform = translateX(Npx)` 会**整个覆盖掉 CSS 里的
      // `translateX(-50%)`**，标签于是跳回未居中的位置 ——
      // 实测 overflowL 从 17.3 变成 22.5，越修越糟。
      const f = first.getBoundingClientRect();
      if (f.left < cb.left - 0.5) {
        first.style.transform =
          'translateX(calc(-50% + ' + (cb.left - f.left).toFixed(1) + 'px))';
      }
      const l = last.getBoundingClientRect();
      if (l.right > cb.right + 0.5) {
        last.style.transform =
          'translateX(calc(-50% + ' + (cb.right - l.right).toFixed(1) + 'px))';
      }
    }
  }
}

/* stHBar 横向条形图（用于主体分布、高频动作——标签长，横向更好读）*/
function stHBar(box, items, color){
  const el = typeof box === 'string' ? $(box) : box;
  if(!el) return;
  if(!items || !items.length){ el.innerHTML = '<div class="hint">'+esc(t('暂无数据'))+'</div>'; return; }
  const max = Math.max.apply(null, items.map(i=>i.v)) || 1;
  el.innerHTML = items.map(i => {
    const pct = (i.v / max * 100).toFixed(1);
    return '<div style="display:flex;align-items:center;gap:8px;margin:4px 0;font-size:12px">'+
      '<span style="width:104px;flex:none;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" '+
      'title="'+esc(i.k)+'">'+esc(i.k)+'</span>'+
      '<span style="flex:1;background:var(--off);border-radius:4px;height:9px;overflow:hidden">'+
      '<i style="display:block;height:100%;width:'+pct+'%;background:'+(i.color||color)+';border-radius:4px"></i></span>'+
      '<b style="width:52px;text-align:right;flex:none;font-variant-numeric:tabular-nums">'+stFull(i.v)+'</b>'+
      '</div>';
  }).join('');
}

async function loadStats(){
  const box = $('#st-kpis');
  if(!box) return;
  const days = ($('#st-days') && $('#st-days').value) || '30';
  // 区间标题跟着下拉走——不更新就会出现「标题写 30 天、图画的是 7 天」
  const rng = $('#st-range'); if(rng) rng.textContent = t('近 {0} 天', days);
  let d = null;
  try{ d = await (await fetch('/api/stats?days='+encodeURIComponent(days))).json(); }
  catch(e){
    box.innerHTML = '<div class="hint">'+esc(t('读取统计失败：{0}', e))+'</div>';
    return;
  }
  const t2 = d.totals || {}, ds = d.days || [];

  /* ---- KPI 卡（v2.7.1：4 张，正好一行）----
   *
   * 去掉「失败」「被拦下」「任务数」三张：
   * 它们的数字**已经在下面的图表里**（结果分布堆叠条 + 图例数字），
   * 再放一张卡是重复；而「Token 消耗」是 v2.7 才有数据的新指标，
   * 样本还只有十几个，摆在最显眼的位置反而像结论。
   *
   * 留这 4 张的理由：它们是**图表画不出来的**——
   * 操作量给分母、复验率给质量、延迟给性能、Token 给成本。
   */
  const vr = t2.verify_rate_has_sample
    ? {v: (t2.verify_rate*100).toFixed(1)+'%', cls:'', s: t('通过 {0} / {1} 次复验', stFull(t2.verify_ok), stFull(t2.verify_count))}
    : {v: t('无样本'), cls:'none', s: t('区间内没有带复验结论的记录')};
  const kpis = [
    {k:t('操作总数'), v: stFull(t2.ops), cls:'', s: t('{0} 天有记录', t2.audit_days_available)},
    {k:t('复验通过率'), v: vr.v, cls: vr.cls, s: vr.s},
    {k:t('延迟 p50'), v: t2.latency_p50? stFull(t2.latency_p50)+' ms': t('无数据'),
     cls: t2.latency_p50?'':'none', s: t('p90 {0} ms', stFull(t2.latency_p90))},
    {k:t('Token 消耗'), v: t2.token_samples? stFmt(t2.token_total): t('无数据'),
     cls: t2.token_samples?'':'none',
     s: t2.token_samples
        ? (t2.token_has_cache && t2.token_prompt
            // 有缓存细分时把命中率写进去 —— 它是判断成本的关键，
            // 而原来那张 token 图被移除后，这个信息不该一起丢
            ? t('缓存命中 {0}%　输入 {1} · 输出 {2}',
                ((t2.token_cache_hit/t2.token_prompt)*100).toFixed(0),
                stFmt(t2.token_prompt), stFmt(t2.token_completion))
            : t('输入 {0} · 输出 {1}', stFmt(t2.token_prompt), stFmt(t2.token_completion)))
        : t('v2.7 起才记录')},
  ];
  box.innerHTML = kpis.map(x =>
    '<div class="kpi '+x.cls+'"><div class="k">'+esc(x.k)+'</div>'+
    '<div class="v">'+esc(x.v)+'</div><div class="s">'+esc(x.s)+'</div></div>').join('');

  /* ---- 口径提醒（无样本必须说出来，不能画成 0）---- */
  const notes = $('#st-notes');
  const ns = d.notices || [];
  if(notes){
    if(ns.length){
      notes.style.display='';
      notes.innerHTML = ns.map(x=>'<li>'+esc(x)+'</li>').join('');
    } else notes.style.display='none';
  }

  /* ---- 操作量与结果 ---- */
  const RKEY = [
    {k:'ok',       color:'var(--ok)',   name:'成功'},
    {k:'fail',     color:'var(--err)',  name:'失败'},
    {k:'denied',   color:'var(--warn)', name:'被拦下'},
    {k:'started',  color:'var(--info)', name:'已启动'},
    // v3.0 T1.6：未证明恢复单列。它进分母（复验率的分母是"复验过次数"），
    // 但不进"失败"——把"还没验到"画成失败，看板就在教人误读。
    {k:'unverified', color:'var(--muted)', name:'未证明恢复'},
  ];
  stBars('#st-ops-chart', ds, RKEY, {height:130});
  const stack = $('#st-ops-stack');
  if(stack){
    const tot = t2.ops || 1;
    stack.innerHTML = RKEY.map(k =>
      '<i style="width:'+((t2[k.k]||0)/tot*100).toFixed(2)+'%;background:'+k.color+'" '+
      'title="'+esc(t(k.name))+': '+stFull(t2[k.k]||0)+'"></i>').join('');
    // 图例后面的数字也更新
    document.querySelectorAll('#panel-audit .lgnv').forEach((el,i)=>{
      el.textContent = stFull(t2[RKEY[i].k]||0);
    });
  }
  const tip = $('#st-ops-tip');
  if(tip) tip.textContent = t('日均 {0} 次', (t2.ops/Math.max(1,t2.audit_days_available)).toFixed(0));

  /* ---- v2.7.1：任务结局与 Token 两张图已从界面移除（DOM 与渲染代码一并删掉）----
   *
   * 删而不只是隐藏：留着渲染代码查空元素是死代码，
   * 而空元素上`getElementById` 返回 null、`.innerHTML=` 会抛——
   * 第一版若只删 DOM 不删代码，页面会报 TypeError。
   *
   * **数据仍从 /api/stats 返回**（stats.go 的统计与测试全部保留），
   * 将来样本够了（轨迹 14 天覆盖、token 样本上百）再加回来即可。
   *
   * token 的缓存命中率挪进了 KPI 副标题 —— 它是成本判断的关键，
   * 值得留下，但不值得单独占一张图。
   */

  /* ---- 主体分布 ---- */
  const ACTOR_NAME = {user:'用户', ai:'AI', guard:'守护', system:'系统', shadow:'影子'};
  const ac = t2.by_actor || {};
  stHBar('#st-actor-chart',
    Object.keys(ac).sort((a,b)=>ac[b]-ac[a])
      .map(k=>({k: t(ACTOR_NAME[k]||k), v: ac[k], color: k==='ai'?'var(--brand)':'var(--info)'})));

  /* ---- 高频动作 Top 8 ---- */
  const acts = t2.actions || {};
  stHBar('#st-action-chart',
    Object.keys(acts).sort((a,b)=>acts[b]-acts[a]).slice(0,8)
      .map(k=>({k, v: acts[k], color:'var(--muted)'})));
}

/* ---------- 启动方式推断展示（v2.1） ---------- */
// 把服务端推断结果与合法候选显示出来，并提供"一键采用"。
// 用户不填也能启动（走推断），填了就永久覆盖——这比让 AI 去猜可靠得多。
async function loadLaunchPlan(){
  const brief = $('#launch-brief'), cands = $('#launch-cands');
  if(!brief || !cands) return;
  let d = null;
  try{ d = await (await fetch('/api/launch')).json(); }catch(e){ return; }
  if(!d || d.ok === false) return;
  const p = d.plan || {};
  const L = [];
  L.push(t('前端：{0}', p.web_script ? ('npm run ' + p.web_script + t('（{0}）', launchSrc(p.web_source)))
                                     : t('未识别，不可自动启动')));
  L.push(t('后端：{0}', p.backend_file ? ('go run ' + p.backend_file) : t('未识别，不可自动启动')));
  if(p.note) L.push(p.note);
  brief.textContent = L.join('\n');
  brief.style.whiteSpace = 'pre-line';

  // 合法候选：点一下就填进输入框，省得手打
  cands.innerHTML = '';
  const all = p.web_all || [];
  if(all.length){
    const lab = document.createElement('span');
    lab.className = 'hint'; lab.textContent = t('合法候选：');
    cands.appendChild(lab);
    for(const s of all){
      const b = document.createElement('button');
      b.className = 'mini'; b.textContent = s;
      b.onclick = ()=>{ const inp = $('#cfg_web_script'); if(inp){ inp.value = s; markDirty(); toast(t('已填入 {0}，记得保存配置', s), 'ok'); } };
      cands.appendChild(b);
    }
  }
  if((p.blocked||[]).length){
    const w = document.createElement('span');
    w.className = 'hint';
    w.style.color = 'var(--warn)';
    w.textContent = t('已按安全规则拒绝：{0}', p.blocked.join('、'));
    cands.appendChild(w);
  }
}

function launchSrc(s){
  return s === 'config' ? t('你指定的') : (s === 'package.json' ? t('package.json 推断') : t('未识别'));
}

/* 侧栏导航不再用状态点：状态看顶部胶囊、步骤条和右侧运行状态卡。 */

// ---------- 记忆与经验（M1 自动经验 / M3 用户记忆） ----------

const LESSON_KIND = {
  repeat_fail:'反复失败', user_fix:'你手动修好的', denied:'已被安全策略拒绝',
  slow:'慢路径', verify_fail:'复验反复失败'
};

// memSyncGate 记忆层总开关关闭时给出显式提示——
// 否则用户会困惑"我明明写了记忆，助手怎么还是不知道"。
function memSyncGate(){
  const box = $('#mem-gate-off');
  if(!box) return;
  const cb = document.getElementById('ai_memory_on');
  box.style.display = (cb && !cb.checked) ? '' : 'none';
}

async function loadMemory(task){
  // 不传 task 时沿用手动输入的试算任务：加/删/切换记忆后仍能看到同一份"本轮"标记
  if(task === undefined){
    const el = $('#mem-preview-task');
    task = el ? el.value.trim() : '';
  }
  task = (task || '').trim();
  const mb = $('#mem-body'), lb = $('#les-body');
  if(!mb || !lb) return;
  let d = null;
  try{ d = await (await fetch('/api/lessons' + (task ? '?task=' + encodeURIComponent(task) : ''))).json(); }
  catch(e){
    mb.innerHTML = '<tr><td colspan="6" class="muted" style="padding:14px;text-align:center">'+esc(t('读取失败'))+'</td></tr>';
    return;
  }
  const hint = $('#mem-preview-hint');
  if(hint) hint.textContent = task ? t('本轮将注入 {0} 条记忆 · {1} 条经验', (d && d.injected_memories) || 0, (d && d.injected_lessons) || 0) : '';
  // 我的记忆
  const ms = (d && d.memories) || [];
  mb.innerHTML = '';
  if(!ms.length){
    mb.innerHTML = '<tr><td colspan="6" class="muted" style="padding:14px;text-align:center">'+esc(t('还没有记忆条目'))+'</td></tr>';
  } else {
    for(const m of ms){
      const tr = document.createElement('tr');
      // 失效的记忆不删除、只标出来——用户需要知道它为什么不再生效
      const dimStale = m.stale ? ' style="opacity:.5"' : '';
      const badge = m.injected ? ' <span class="mini" style="color:var(--ok);border-color:var(--ok)">'+esc(t('本轮'))+'</span>' : '';
      const why = (!m.injected && !m.stale && task) ? '<div class="mini muted" style="margin-top:3px">'+esc(t('未注入：{0}', m.reason || ''))+'</div>' : '';
      tr.title = m.stale ? (m.stale_why || '') : (m.reason || '');
      tr.innerHTML =
        '<td style="text-align:center;color:var(--warn);font-weight:700">'+(m.always?'★':'')+'</td>'+
        '<td'+dimStale+'>'+esc(m.text||'')+(m.stale?' <span class="mini" style="color:var(--warn)">['+esc(t('已失效'))+']</span>':'')+badge+why+'</td>'+
        '<td class="mini muted">'+esc(m.tags||'')+'</td>'+
        '<td class="mini muted">'+esc((m.created||'').slice(5,16))+'</td>'+
        '<td class="mini"></td>'+
        '<td></td>';
      // 范围列：单按钮切换（本项目 / 全局）
      const scopeTd = tr.children[4];
      const sb = document.createElement('button');
      sb.className = 'mini' + (m.global ? ' on' : '');
      sb.textContent = m.global ? t('全局') : t('本项目');
      sb.title = m.global ? t('任何项目都生效') : t('换个项目后自动失效');
      sb.onclick = async ()=>{ await memPost({op:'global', id:m.id, global:!m.global}); loadMemory(); };
      scopeTd.appendChild(sb);
      const td = tr.lastElementChild;
      td.style.whiteSpace = 'nowrap';
      const b1 = document.createElement('button');
      b1.className = 'mini'; b1.textContent = m.always ? t('仅相关时') : t('每轮注入');
      b1.style.marginRight = '4px';
      b1.onclick = async ()=>{ await memPost({op:'toggle', id:m.id}); loadMemory(); };
      const b2 = document.createElement('button');
      b2.className = 'mini'; b2.textContent = t('删除');
      b2.onclick = async ()=>{ if(!confirm(t('确定删除这条记忆？'))) return; await memPost({op:'delete', id:m.id}); loadMemory(); };
      td.appendChild(b1); td.appendChild(b2);
      mb.appendChild(tr);
    }
  }
  // 自动经验
  const ls = (d && d.lessons) || [];
  lb.innerHTML = '';
  if(!ls.length){
    lb.innerHTML = '<tr><td colspan="7" class="muted" style="padding:14px;text-align:center">'+esc(t('暂无经验，失败与被拒的记录累积到一定量后会自动出现'))+'</td></tr>';
    return;
  }
  for(const l of ls){
    const tr = document.createElement('tr');
    const dim = l.enabled ? '' : ' style="opacity:.45"';
    const ev = (l.evidence && l.evidence.length) ? '<div class="mini muted" style="margin-top:3px">'+esc(t('依据：{0}', l.evidence.join('　·　')))+'</div>' : '';
    // 成败：0/0 时显示占位而不是 0/0 —— 没人提取到新经验时会误导成"零次"
    const of = (l.succ||0) + '/' + (l.fail||0);
    const ofText = (l.succ||l.fail) ? of : '—';
    // 置信度分档着色，让"仅供参考"在视觉上就弱一档
    const cf = l.conf_label || '';
    const cfColor = cf==='高' ? 'var(--ok)' : (cf==='中' ? 'var(--warn)' : 'var(--muted)');
    // 适用范围 + 失效理由
    let scopeCell = esc(scopeLabelOf(l));
    if(l.stale){
      scopeCell = '<span style="color:var(--warn)">'+esc(t('已失效'))+'</span>';
      if(l.stale_why) scopeCell += '<div class="mini muted" style="margin-top:2px">'+esc(l.stale_why)+'</div>';
    }
    tr.innerHTML =
      '<td class="mini">'+esc(LESSON_KIND[l.kind] || l.kind || '')+'</td>'+
      '<td'+dim+'>'+esc(l.title||'')+(l.injected?' <span class="mini" style="color:var(--ok);border-color:var(--ok)">'+esc(t('本轮'))+'</span>':'')+ev+'</td>'+
      '<td class="mini" style="text-align:center;white-space:nowrap">'+esc(ofText)+'</td>'+
      '<td class="mini" style="text-align:center;white-space:nowrap;color:'+cfColor+'">'+esc(cf||'—')+'</td>'+
      '<td class="mini">'+scopeCell+'</td>'+
      '<td class="mini" style="text-align:center">'+esc(String(l.hits||0))+'</td>'+
      '<td></td>';
    const td = tr.lastElementChild;
    const b = document.createElement('button');
    b.className = 'mini' + (l.enabled ? ' on' : '');
    b.textContent = l.enabled ? t('已采用') : t('已否决');
    b.onclick = async ()=>{
      const r = await memPost({op:'__lesson', id:l.id}, '/api/lesson/toggle');
      if(r && r.ok) loadMemory();
    };
    td.appendChild(b);
    lb.appendChild(tr);
  }
}

// scopeLabelOf 把经验的作用域压成一行短文本。
function scopeLabelOf(l){
  const sc = l.scope || {};
  const parts = [];
  const who = [sc.frontend, sc.backend].filter(Boolean).join('/');
  parts.push(who || t('本项目'));
  if(sc.action) parts.push(sc.action);
  return parts.join(' · ');
}

async function memPost(body, url){
  url = url || '/api/memory';
  try{
    const r = await (await fetch(url, {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)})).json();
    if(r && r.ok === false && r.error) toast(r.error, 'err');
    return r;
  }catch(e){ toast(t('请求失败'), 'err'); return null; }
}

function memBind(){
  const add = $('#mem-add');
  if(add) add.onclick = async ()=>{
    const inp = $('#mem-text'), tg = $('#mem-tags'), al = $('#mem-always'), gl = $('#mem-global');
    const text = (inp.value||'').trim();
    if(!text){ toast(t('请先填写记忆内容'), 'err'); return; }
    const r = await memPost({op:'add', text: text, tags:(tg.value||'').trim(), always: !!(al && al.checked)});
    if(r && r.ok){
      inp.value=''; tg.value='';
      if(al) al.checked=false;
      // 全局勾选保持原状：用户可能正在批量写通用规矩
      loadMemory();
    }
    if(gl && gl.checked) toast(t('已记为全局适用（换项目也生效）'), 'ok');
  };
  const ib = $('#mem-import-btn');
  if(ib) ib.onclick = async ()=>{
    const text = prompt(t('粘贴要导入的记忆，每行一条：'));
    if(!text) return;
    const r = await memPost({text: text}, '/api/memory/import');
    if(r && r.ok){ toast(r.msg, 'ok'); loadMemory(); }
  };
  const rf = $('#mem-refresh');
  if(rf) rf.onclick = loadMemory;
  // 试算：用真实的选择逻辑跑一遍，告诉用户"这句话会带哪些记忆/经验、为什么没带"
  const pv = $('#mem-preview');
  if(pv) pv.onclick = ()=> loadMemory(($('#mem-preview-task') || {}).value || '');
  const pvi = $('#mem-preview-task');
  if(pvi) pvi.addEventListener('keydown', e=>{ if(e.key === 'Enter'){ e.preventDefault(); loadMemory(pvi.value || ''); } });
  const toggle = $('#btn-ai-memory');
  if(toggle) toggle.onclick = ()=>{
    const box = $('#ai-memory-box');
    if(!box) return;
    const show = box.style.display === 'none';
    box.style.display = show ? '' : 'none';
    if(show){ memSyncGate(); loadMemory(); }
  };
  // 记忆层总开关：勾选状态变化即时反映到面板提示（改动会自动保存，无需手动点保存）
  const gate = document.getElementById('ai_memory_on');
  if(gate) gate.addEventListener('change', memSyncGate);
  memSyncGate();
}
async function pollHealth(){
  let h; try{ h = await (await fetch('/api/health')).json(); }catch(e){ return; }
  /* 忙碌互斥 */
  busyNow = h.busy || '';
  const bb = $('#busy-banner');
  if(busyNow){ bb.style.display=''; bb.textContent=t('正在执行：{0} …（完成前其它操作已禁用）', busyNow); }
  else { bb.style.display='none'; }
  /* 顶部状态条 */
  if(!h.env.hasResults) chip('chip-env','off','环境 未检测');
  else chip('chip-env', h.env.ready?'ok':'err', h.env.ready?'环境 就绪':'环境 缺失');
  if(h.db.connected) chip('chip-db', h.db.dbExists?'ok':'warn', h.db.dbExists?'数据库 就绪':'数据库 未建库');
  else if(h.db.msg==='未检测') chip('chip-db','off','数据库 未检测');
  else chip('chip-db','err','数据库 连接失败');
  const depsOk = h.deps.frontend && h.deps.backend;
  chip('chip-deps', depsOk?'ok':'warn', depsOk?'依赖 就绪':'依赖 待办');
  /* v2.7 状态灯以 verified 为条件，不再只看 running。
   *
   * **改之前**：svcOk = web.running || backend.running
   * 那样端口被别人的服务占着、进程在但没通过复验，一样显示绿——
   * **等于把「执行不等于成功」在视觉上抹掉**，
   * 前面所有验证层的努力在界面上等于零。
   *
   * 现在：running 但未 verified → 黄（降级），明确区分「没起来」与「起来了但没验过」。
   */
  const svcState = aggregateSvcState(h.services);
  chip('chip-svc', svcState.cls, svcState.label);
  /* 权限 */
  $('#chip-admin').style.display = h.admin?'':'none';
  $('#btn-elevate').style.display = h.admin?'none':'';
  /* 侧栏导航已不再用状态点：状态看顶部胶囊、步骤条和右侧运行状态卡 */
  /* 服务卡：同样以 verified 为条件 */
  const w=h.services.web, b=h.services.backend;
  const sw=$('#su-web'), sb=$('#su-backend');
  sw.textContent = svcLabel(w); sw.className = 'svcstate' + svcDotCls(w);
  sb.textContent = svcLabel(b); sb.className = 'svcstate' + svcDotCls(b);
  const lw=$('#sl-web');
  if(w.running && w.url){ lw.style.display=''; lw.href=w.url; lw.textContent=t('打开 ↗'); lw.title=w.url; }
  else { lw.style.display='none'; }
  const lb=$('#sl-backend');
  if(b.running && b.url){ lb.style.display=''; lb.href=b.url; lb.textContent=t('打开 ↗'); lb.title=b.url; }
  else { lb.style.display='none'; }
  applyBreaker('web', w);
  applyBreaker('backend', b);
  $('#btn-stop-all').disabled = !svcState.running;
  $('#db-test-msg').textContent = h.db.connected ? (h.db.Version||'') + (h.db.dbExists?' · 库已存在':' · 库不存在') : (h.db.msg||'');
  updateChain(h.chain);
  /* 问题清单（状态源算好的，含「该怎么办」） */
  renderIssues(h.issues);
  /* 四步进度指引：done=该步就绪；第一个未就绪的步高亮为"当前步" */
  const chainDone = !!(h.chain && h.chain.checked);
  const gSteps = [
    ['gs-1', h.env.hasResults && h.env.ready],
    ['gs-2', h.db.connected && h.db.dbExists && depsOk],
    ['gs-3', chainDone],
    ['gs-4', svcState.running]
  ];
  let gCur = false;
  gSteps.forEach(([id, done])=>{
    const el = document.getElementById(id); if(!el) return;
    el.classList.toggle('done', !!done);
    const cur = !done && !gCur; gCur = gCur || cur;
    el.classList.toggle('cur', cur);
  });
  refreshGuards();
}

/* ---------- 服务状态的呈现：running 与 verified 分开 ---------- */

/* applyBreaker 熔断可见性 + 复位入口。
 *
 * 为什么必须单独显示：熔断时服务确实是"没起来"，但原因是**产品拦住了崩溃循环**，
 * 不是启动失败。两者混成同一种红，用户只会反复点"启动"，而每一次都被断路器拒绝
 * ——界面不把拒绝说出来，就等于让用户跟产品对着撞。
 *
 * 复位按钮必须有人工出口：冷静期只是节流，不是监狱。 */
function applyBreaker(key, s){
  const row = document.getElementById('brk-' + key);
  if(!row) return;
  if(!s || !s.breaker_tripped){ row.style.display='none'; return; }
  row.style.display='';
  let txt = document.querySelector('#brk-' + key + ' .brk-txt');
  if(!txt){ txt = document.createElement('span'); txt.className='brk-txt'; row.insertBefore(txt, row.firstChild); }
  const secs = s.breaker_remain_secs || 0;
  txt.textContent = secs > 0
    ? t('已停止自动重启（连续失败达上限）· {0} 秒后放行一次探针', secs)
    : t('已停止自动重启（连续失败达上限）');
}

/* 复位断路器：人工确认崩溃原因已处理后解除熔断。
 * 必须 POST 到后端而不是本地改变量——否则界面说"已复位"而断路器还在拦，
 * 那是界面在替产品撒谎。走统一的 post()，失败由它弹 toast。 */
document.querySelectorAll('[data-brk]').forEach(btn=>{
  btn.addEventListener('click', async ()=>{
    const key = btn.getAttribute('data-brk');
    btn.disabled = true;
    const r = await post('/api/program/breaker-reset', {service:key});
    if(r && r.ok){
      let j = {}; try{ j = await r.json(); }catch(e){}
      toast(j.reset ? t('断路器已复位，恢复接受自动重启') : t('当前不在熔断态，无需复位'), j.reset ? 'ok' : '');
    }
    btn.disabled = false;
    pollHealth();
  });
});

/* svcDotCls 单个服务的状态点颜色。
 * 绿 = 跑着**且验过**；黄 = 跑着但没验过（或在启动中）；红 = 失败；灰 = 停止。
 * 后端 ServiceState.phase 已给出五态，这里只做映射，不再自己推断。 */
function svcDotCls(s){
  if(!s) return '';
  if(s.phase==='failed') return ' err';
  if(s.phase==='degraded') return ' warn';   // 进程在但未通过复验
  if(s.phase==='starting') return ' warn';
  if(s.phase==='running') return s.verified ? ' ok' : ' warn';
  return '';
}

/* svcLabel 服务状态文案。**degraded 必须说清是「没验过」**，
 * 显示「运行中」等于把未验证说成已验证。 */
function svcLabel(s){
  if(!s) return t('未启动');
  switch(s.phase){
    case 'running': return s.verified ? t('运行中 · 已复验') : t('运行中 · 未复验');
    case 'starting': return t('启动中');
    case 'degraded': return t('降级 · 未通过复验');
    case 'failed': return t('失败');
    default: return t('未启动');
  }
}

/* aggregateSvcState 汇总两个服务成一个顶部灯。
 * 口径：**全部已复验才绿**；有跑着的但没验过 → 黄；全停 → 灰。 */
function aggregateSvcState(services){
  const list = [services && services.web, services && services.backend].filter(Boolean);
  if(!list.length) return {cls:'off', label:t('服务 未启动'), running:false};
  const running = list.some(s => s.running);
  const failed  = list.some(s => s.phase === 'failed');
  const allVerified = list.every(s => s.verified);
  if(failed) return {cls:'err', label:t('服务 异常'), running:running};
  if(running && allVerified) return {cls:'ok', label:t('服务 已就绪'), running:true};
  if(running) return {cls:'warn', label:t('服务 未复验'), running:true};
  return {cls:'off', label:t('服务 未启动'), running:false};
}

/* renderIssues 渲染状态源算好的问题清单。
 * 每条带「该怎么办」——只报问题不给下一步，等于把负担推回给用户。 */
function renderIssues(issues){
  const box = $('#issues-box'), list = $('#issues-list');
  if(!box || !list) return;
  const arr = Array.isArray(issues) ? issues : [];
  if(!arr.length){ box.style.display='none'; return; }
  box.style.display='';
  list.innerHTML = arr.map(i=>{
    const cls = i.level==='error' ? 'err' : (i.level==='warn' ? 'warn' : 'info');
    return '<li class="'+cls+'"><b>'+esc(i.what)+'</b>'+
           (i.action ? '<span class="act">'+esc(t('建议：{0}', i.action))+'</span>' : '')+'</li>';
  }).join('');
}

/* ---------- 链端校验 ---------- */
function updateChain(ch){
  ch = ch || {};
  const dot=(id,on)=>{ const d=document.getElementById(id); if(d) d.className='dot'+(on?' ok':''); };
  dot('cd-20200', ch.checked && ch.port20200);
  dot('cd-5002', ch.checked && ch.port5002);
  const cs20200 = document.getElementById('cs-20200');
  if(!ch.checked){ cs20200.textContent='未检测'; }
  else if(ch.port20200 && ch.chainExtReach){ cs20200.textContent=t('可达 · {0}ms', ch.lat20200||0); }
  else if(ch.port20200 && ch.chainBindLocal){ cs20200.textContent='本地监听（公网不可达·已加固）'; }
  else { cs20200.textContent='不通'; }
  document.getElementById('cs-5002').textContent = !ch.checked ? '未检测' : (ch.port5002? t('可达 · {0}ms', ch.lat5002||0) : '不通');
  document.getElementById('m-block').textContent = ch.blockNumber || '—';
  document.getElementById('m-tx').textContent = ch.txCount || '—';
  document.getElementById('m-ver').textContent = ch.clientVer || '—';
  const mp=document.getElementById('m-procs');
  if(mp) mp.textContent = !ch.checked ? '—' : (ch.nodeProcs===undefined || ch.nodeProcs<0 ? '未知' : t('{0} 个存活', ch.nodeProcs));
  const mr=document.getElementById('m-recover');
  if(mr) mr.textContent = (ch.recCount>0) ? t('共 {0} 次 · 最近 {1}', ch.recCount, ch.lastRecover) : (ch.checked ? '未发生' : '—');
  const wl=document.getElementById('m-webase');
  if(ch.webaseUrl){ wl.href=ch.webaseUrl; wl.textContent=ch.webaseUrl; } else { wl.removeAttribute('href'); wl.textContent='—'; }
  document.getElementById('chain-at').textContent = ch.at ? t('检测于 {0}', ch.at) : '';
  const st=document.getElementById('st-chain');
  if(!ch.checked){ st.className='status idle'; st.textContent='未检测'; }
  else if(ch.port20200 && ch.port5002){ st.className='status ok'; st.textContent='正常'; }
  else if(ch.port20200 && !ch.port5002){ st.className='status warn'; st.textContent='节点正常·WeBASE未运行'; }
  else { st.className='status fail'; st.textContent='节点未运行'; }
  const ss=document.getElementById('st-ssh');
  if(ch.sshMsg){ ss.className='status '+(ch.sshOk?'ok':'fail'); ss.textContent = ch.sshOk ? '连接成功' : '失败'; }
  document.getElementById('ssh-node').textContent = ch.nodeInfo || '';
  dot('sd-webase', ch.checked && ch.port5002);
  const su=document.getElementById('su-webase');
  if(su){ su.textContent = !ch.checked ? '未检测' : (ch.port5002 ? (ch.webaseOk?'正常':'可达') : '不通');
    su.className = 'svcstate' + (!ch.checked ? '' : (ch.port5002 ? ' ok' : ' err')); }
  const sl=document.getElementById('sl-webase');
  if(sl){ if(ch.webaseUrl && ch.checked && ch.port5002){ sl.style.display=''; sl.href=ch.webaseUrl; sl.textContent=t('打开 ↗'); sl.title=ch.webaseUrl; } else { sl.style.display='none'; } }
  const nd=document.getElementById('nd-chain');
  if(nd) nd.className='dot'+(ch.checked ? (ch.port20200 ? (ch.port5002?' ok':' warn') : ' fail') : '');
}
$('#btn-ssh-test').onclick = ()=>post('/api/chain/ssh-test');
$('#btn-ssh-term').onclick = ()=>post('/api/chain/terminal');
$('#btn-ssh-exec').onclick = ()=>{ const c=$('#ssh_cmd').value.trim(); if(!c){ alert('请输入要执行的命令'); return; } post('/api/chain/exec',{cmd:c}); };
$('#btn-chain-check').onclick = ()=>post('/api/chain/check');
$('#btn-start-chain').onclick = async ()=>{ if(await askConfirm('启动区块链节点',t('将通过 SSH 在链端目录执行启动脚本：\n{0} && {1}\n\n可能耗时较久。', $('#ch_dir').value.trim(), $('#ch_start').value.trim()),'启动')) post('/api/chain/start',{target:'chain'}); };
$('#btn-start-webase').onclick = async ()=>{ if(await askConfirm('启动 WeBASE-Front',t('将通过 SSH 在 WeBASE 目录执行启动脚本：\n{0} && {1}', $('#ch_wdir').value.trim(), $('#ch_wstart').value.trim()),'启动')) post('/api/chain/start',{target:'webase'}); };

/* 启动菜单：选完就收；点外面也收——否则下拉会一直挂在操作面上 */
(function(){
  const m = document.getElementById('start-menu');
  if(!m) return;
  const close = ()=>{ m.open = false; };
  m.querySelectorAll('button').forEach(b=>b.addEventListener('click', close));
  document.addEventListener('click', e=>{ if(m.open && !m.contains(e.target)) close(); });
})();

/* ---------- 程序配置 / 启动 ---------- */
const post=async (url,body)=>{
  try{
    const r = await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body||{})});
    if(!r.ok){
      // 以前是 fire-and-forget：后端 400/500 用户毫无感知。现在统一弹 toast（428 确认流由 postConfirm 自行处理）
      let msg = 'HTTP ' + r.status;
      try{ const j = await r.json(); if(j.reason) msg = j.reason; else if(j.error) msg = j.error; else if(j.problems && j.problems.length) msg = j.problems.map(p=>p.field+' → '+p.msg).join('；'); }catch(e){}
      toast(t('操作失败：') + msg, 'err');
    }
    return r;
  }catch(e){ toast(t('网络错误：') + e, 'err'); return null; }
};
// postConfirm：先探一次，服务端若判定为破坏性操作会返回 428 + 风险说明（含 SQL 高危语句扫描结果），
// 弹窗用的是服务端给的真实风险，比前端写死的提示更准确；确认后才带 confirm:true 重发。
async function postConfirm(url, body, title, fallback){
  const withLang = {...(body||{}), lang: (typeof EK_LANG!=='undefined' ? EK_LANG : 'zh')};
  const r = await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(withLang)});
  if(r.status !== 428) return r;
  let j = {}; try{ j = await r.json(); }catch(e){}
  /* 风险分级：服务端标记 highrisk（drop/truncate/覆盖库等）或文案含高危字样 → 红色警示样式；
     常规确认保持精简，避免确认疲劳 */
  const danger = !!j.highrisk || /高危|不可撤销|覆盖库/.test(j.reason || '');
  if(await askConfirm(title, j.reason || fallback, t('确认执行'), danger)){
    return fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...(body||{}), confirm:true})});
  }
  return null;
}
$('#btn-db-test').onclick = ()=>post('/api/program/db-test');
$('#btn-init-db').onclick = async ()=>{
  const db=$('#db_name').value.trim(), sql=$('#sql_path').value.trim();
  const msg = sql ? t('将创建数据库 {0}，并执行 SQL：\n{1}\n\n注意：若库中已有同名表，可能被覆盖/追加。', db, sql) : t('将创建数据库 {0}（未选 SQL，仅建库）。', db);
  if(await askConfirm(t('初始化数据库'), msg, t('开始执行'))) post('/api/program/init-db',{db, sql});
};
$('#btn-apply-sql-only').onclick = async ()=>{
  const p=$('#sql_path').value.trim(); if(!p){ alert(t('请先选择 SQL 文件')); return; }
  await postConfirm('/api/program/apply-sql', {path:p, db:$('#db_name').value.trim()},
    t('执行 SQL'), t('将对数据库 {0} 执行：\n{1}\n\n注意：可能覆盖/追加已有数据。', $('#db_name').value.trim(), p));
};
$('#btn-web-install').onclick = ()=>post('/api/program/web-install',{dir:$('#fe_dir').value.trim()});
$('#btn-backend-tidy').onclick = ()=>post('/api/program/backend-tidy',{dir:$('#be_dir').value.trim()});
/* 停止→启动宽限：端口释放需要时间，刚停止就点启动会失败；3 秒内自动延迟 */
const lastStopTs = {};
function stopPost(key){ lastStopTs[key||'all'] = Date.now(); return post('/api/program/stop', key?{key}:{}); }
function guardedStart(key, fn){
  const last = lastStopTs[key] || lastStopTs.all || 0;
  const gap = Date.now() - last;
  if(gap < 3000){
    const wait = Math.ceil((3000-gap)/1000);
    toast(t('刚停止，等 {0} 秒让端口释放后自动启动…', wait));
    setTimeout(fn, 3000-gap);
  } else fn();
}
$('#btn-web-start').onclick = ()=>guardedStart('web', ()=>post('/api/program/web-start',{dir:$('#fe_dir').value.trim(), script:$('#web_script').value}));
$('#btn-backend-start').onclick = ()=>guardedStart('backend', ()=>post('/api/program/backend-start',{dir:$('#be_dir').value.trim()}));
$('#btn-web-stop').onclick = ()=>stopPost('web');
$('#btn-backend-stop').onclick = ()=>stopPost('backend');
$('#btn-stop-all').onclick = ()=>stopPost('');
$('#btn-quick-web').onclick = ()=>$('#btn-web-start').click();
$('#btn-quick-backend').onclick = ()=>$('#btn-backend-start').click();
$('#btn-quick-both').onclick = ()=>{ $('#btn-quick-web').click(); setTimeout(()=>$('#btn-quick-backend').click(), 800); };

/* ---------- 端口占用诊断 + 自定义启动脚本 ---------- */
function psPorts(){
  if(!cfg.projects) cfg.projects={};
  if(!Array.isArray(cfg.projects.scan_ports) || !cfg.projects.scan_ports.length) cfg.projects.scan_ports=[8080,8888];
  var spj=cfg.projects.scan_ports.slice().sort(function(a,b){return a-b;}).join();
  if(spj==='20200,3306,8080'||spj==='20200,8080') cfg.projects.scan_ports=[8080,8888]; // 旧默认迁移（3306/20200 → 8888）
  return cfg.projects.scan_ports;
}
function psRenderChips(){
  const box=$('#ps-chips'); box.textContent='';
  psPorts().forEach(p=>{
    const c=document.createElement('span'); c.className='chip'; c.style.cursor='default'; c.textContent=p;
    const x=document.createElement('span'); x.textContent=' ×'; x.style.cursor='pointer'; x.style.opacity='.6';
    x.onclick=()=>{ cfg.projects.scan_ports=psPorts().filter(v=>v!==p); markDirty(); psRenderChips(); };
    c.appendChild(x); box.appendChild(c);
  });
}
function psAddPort(){
  const v=parseInt($('#ps-new').value,10);
  if(!v || v<1 || v>65535){ toast(t('无效端口号'),'err'); return; }
  if(psPorts().includes(v)){ toast(t('该端口已在列表中'),'err'); return; }
  cfg.projects.scan_ports.push(v); $('#ps-new').value=''; markDirty(); psRenderChips();
}
function psSetSt(s,cls){ const el=$('#st-portscan'); el.textContent=s; el.className='status '+(cls||'idle'); }
async function psScan(){
  psSetSt(t('扫描中…'),'run');
  try{
    const j=await (await fetch('/api/ports/scan?ports='+psPorts().join(','))).json();
    const rows=j.ports||[];
    psRenderResult(rows);
    const occ=rows.filter(p=>p.state==='LISTENING').length;
    psSetSt(occ>0 ? t('{0} 个被占用', occ) : t('全部空闲'), occ>0 ? 'warn' : 'ok');
  }catch(e){ psSetSt(t('扫描失败'),'fail'); toast(t('扫描失败：{0}', e.message),'err'); }
}
function psRenderResult(rows){
  const box=$('#ps-result'); box.textContent='';
  if(!rows.length) return;
  const tbl=document.createElement('table'); tbl.style.cssText='width:100%;font-size:12.5px;border-collapse:collapse';
  const mk=(txt,head,color)=>{ const c=document.createElement(head?'th':'td'); c.textContent=txt;
    c.style.cssText=(head?'text-align:left;':'')+'padding:4px 8px;'; if(color) c.style.color=color; else if(head) c.style.color='var(--muted)'; return c; };
  const hr=document.createElement('tr');
  [t('端口'),t('状态'),'PID',t('进程名'),''].forEach(x=>hr.appendChild(mk(x,true)));
  tbl.appendChild(hr);
  rows.forEach(r=>{
    const tr=document.createElement('tr');
    tr.appendChild(mk(r.port));
    if(r.state==='LISTENING'){
      tr.appendChild(mk(t('占用'), false, '#e5484d'));
      tr.appendChild(mk(r.pid)); tr.appendChild(mk(r.name||'—'));
      const td=document.createElement('td'); td.style.padding='4px 8px';
      const b=document.createElement('button'); b.className='warn'; b.style.padding='2px 10px'; b.textContent=t('结束');
      b.onclick=()=>psKill(r); td.appendChild(b); tr.appendChild(td);
    } else {
      tr.appendChild(mk(t('空闲'), false, '#2fb344'));
      tr.appendChild(mk('—')); tr.appendChild(mk('—')); tr.appendChild(mk(''));
    }
    tbl.appendChild(tr);
  });
  box.appendChild(tbl);
}
async function psKill(r){
  const okgo = await askConfirm(t('结束进程'),
    t('将强制结束进程 {0}（PID {1}，占用端口 {2}），未保存的数据可能丢失。请确认它不是系统关键进程。', r.name||t('未知进程'), r.pid, r.port), t('结束'));
  if(!okgo) return;
  try{
    const res=await fetch('/api/ports/kill',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({pid:r.pid, port:r.port, confirm:true, lang:(EK_LANG==='en'?'en':'zh')})});
    if(!res.ok){ const tx=await res.text(); throw new Error(tx||('HTTP '+res.status)); }
    toast(t('已结束 PID {0}，可重新扫描确认', r.pid));
    setTimeout(psScan, 800);
  }catch(e){ toast(t('结束失败：{0}', e.message),'err'); }
}
$('#btn-ps-add').onclick = psAddPort;
$('#ps-new').addEventListener('keydown', e=>{ if(e.key==='Enter'){ e.preventDefault(); psAddPort(); } });
$('#btn-ps-scan').onclick = psScan;

function wlList(){
  if(!cfg.projects) cfg.projects={};
  if(!Array.isArray(cfg.projects.web_scripts)) cfg.projects.web_scripts=[];
  return cfg.projects.web_scripts;
}
function validScript(s){ return /^[\w:.\/-]{1,64}$/.test(s); }
function wlRender(){
  const dl=$('#wl-scripts'); dl.textContent='';
  ['serve','dev'].concat(wlList()).forEach(s=>{ const o=document.createElement('option'); o.value=s; o.textContent='npm run '+s; dl.appendChild(o); });
}
function wlRemember(silent){
  const v=$('#web_script').value.trim();
  if(!v) return v;
  if(!validScript(v)){ toast(t('脚本名含非法字符'),'err'); return v; }
  if(!['serve','dev'].includes(v) && !wlList().includes(v)){
    wlList().push(v); markDirty();
    if(!silent) toast(t('已收藏脚本 {0}', v));
  }
  try{ localStorage.setItem('ek-web-script', v); }catch(_){}
  return v;
}
$('#btn-script-save').onclick = ()=>wlRemember(false);

/* ---------- 数据浏览（只读） ---------- */
const dbv = { open:false, tab:'rows', db:null, table:null, page:1, size:50,
  dbs:[], tables:[], counts:{}, cols:[], rows:[], mode:'table' };

async function dbvFetch(url, opts){
  const r = await fetch(url, opts);
  const t = await r.text();
  let j = null; try{ j = JSON.parse(t); }catch(_){}
  if(!r.ok || (j && j.error)) throw new Error((j && j.error) || t || ('HTTP ' + r.status));
  return j;
}

function dbvSetSt(s, cls){ const el=$('#st-dbview'); el.textContent=s; el.className='status '+(cls||'idle'); }

function dbvRenderSide(){
  const side = $('#dbv-side'); side.textContent = '';
  const mk = (label, cls, cnt, onclick) => {
    const d = document.createElement('div');
    d.className = 'dbv-item ' + cls;
    const span = document.createElement('span'); span.textContent = label;
    d.appendChild(span);
    if(cnt !== null && cnt !== undefined){ const c=document.createElement('span'); c.className='cnt'; c.textContent=cnt; d.appendChild(c); }
    if(onclick) d.onclick = onclick;
    side.appendChild(d);
    return d;
  };
  if(!dbv.dbs.length){ mk(t('未检测到数据库（点「刷新」重试）'), 'db', null, null); return; }
  for(const d of dbv.dbs){
    const cur = d === dbv.db;
    mk(d, 'db' + (cur ? ' cur' : ''), null, ()=>{ dbv.db = cur ? null : d; dbv.table = null; dbv.tables = []; dbv.counts = {}; dbv.render(); if(dbv.db) dbvLoadTables(); });
    if(cur){
      if(dbv.tables.length === 0 && dbv.tablesLoaded){ mk(t('（无表）'), 'tb', null, null); }
      for(const tb of dbv.tables){
        const c = dbv.counts[tb];
        mk(tb, 'tb' + (tb === dbv.table ? ' cur' : ''), c !== undefined ? c : '…', ()=>{ dbv.table = tb; dbv.page = 1; dbv.mode = 'table'; dbvTabSwitch('rows'); dbv.render(); });
      }
    }
  }
}

dbv.render = function(){ dbvRenderSide(); };

async function dbvLoadDbs(){
  dbvSetSt(t('读取中…'), 'run');
  try{
    const j = await dbvFetch('/api/db/list');
    dbv.dbs = j.databases || [];
    dbvSetSt(t('已打开'), 'ok');
  }catch(e){ dbv.dbs = []; dbvSetSt(t('连接失败'), 'fail'); toast(t('数据库列表读取失败：{0}', e.message), 'err'); }
  dbvRenderSide();
}

async function dbvLoadTables(){
  if(!dbv.db) return;
  try{
    const j = await dbvFetch('/api/db/tables?db=' + encodeURIComponent(dbv.db));
    dbv.tables = j.tables || []; dbv.counts = j.counts || {}; dbv.tablesLoaded = true;
  }catch(e){ dbv.tables = []; dbv.counts = {}; toast(t('表列表读取失败：{0}', e.message), 'err'); }
  dbvRenderSide();
}

async function dbvLoadRows(){
  if(!dbv.db || !dbv.table) return;
  const grid = $('#dbv-grid'); grid.textContent = '';
  const tip = document.createElement('tr'); const td = document.createElement('td');
  td.textContent = t('加载中…'); td.colSpan = 1; tip.appendChild(td); grid.appendChild(tip);
  try{
    const j = await dbvFetch(`/api/db/rows?db=${encodeURIComponent(dbv.db)}&table=${encodeURIComponent(dbv.table)}&page=${dbv.page}&size=${dbv.size}`);
    dbv.cols = j.cols || []; dbv.rows = j.rows || [];
    dbvRenderGrid();
    const total = parseInt(j.total, 10) || 0;
    const pages = Math.max(1, Math.ceil(total / dbv.size));
    $('#dbv-pageinfo').textContent = t('共 {0} 行 · 第 {1}/{2} 页', total.toLocaleString(), dbv.page, pages);
  }catch(e){ grid.textContent = ''; const tr=document.createElement('tr'), c=document.createElement('td');
    c.textContent = t('查询失败：{0}', e.message); c.style.color='#e5484d'; c.style.padding='10px'; tr.appendChild(c); grid.appendChild(tr); }
}

function dbvRenderGrid(){
  const grid = $('#dbv-grid'); grid.textContent = '';
  if(!dbv.cols.length){
    const tr=document.createElement('tr'), c=document.createElement('td');
    c.textContent = dbv.tab === 'rows' ? t('请先在左侧选择一张表') : t('（空结果）');
    c.style.cssText='padding:14px;color:var(--muted)'; tr.appendChild(c); grid.appendChild(tr); return;
  }
  const thead = document.createElement('thead'); const hr = document.createElement('tr');
  for(const c of dbv.cols){ const th=document.createElement('th'); th.textContent=c; hr.appendChild(th); }
  thead.appendChild(hr); grid.appendChild(thead);
  const tbody = document.createElement('tbody');
  for(const rw of dbv.rows){
    const tr = document.createElement('tr');
    for(let i=0;i<dbv.cols.length;i++){
      const td = document.createElement('td');
      const v = rw[i] === undefined ? '' : rw[i];
      if(v === 'NULL'){ td.textContent = 'NULL'; td.classList.add('null'); }
      else { td.textContent = v; if(v === '') td.innerHTML = '&nbsp;'; }
      td.onclick = ()=>{ const box=$('#dbv-cell'); box.style.display=''; box.textContent = (dbv.cols[i] + ' = ') + (v === 'NULL' ? 'NULL' : v); };
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  }
  grid.appendChild(tbody);
}

function dbvRunSQL(){
  const sql = $('#dbv-sql').value.trim(); if(!sql) return;
  $('#h-dbv-sql').textContent = t('运行中…');
  dbvFetch('/api/db/query', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({db: dbv.db || '', sql})})
    .then(j=>{ $('#h-dbv-sql').textContent = t('{0} 行', (j.rows||[]).length); dbv.cols = j.cols||[]; dbv.rows = j.rows||[]; dbvRenderGrid(); $('#dbv-pageinfo').textContent = t('自定义查询 · {0} 行', (j.rows||[]).length); })
    .catch(e=>{ $('#h-dbv-sql').textContent = ''; toast(t('查询失败：{0}', e.message), 'err'); });
}

function dbvExportCSV(){
  if(!dbv.cols.length){ toast(t('没有可导出的数据'), 'err'); return; }
  const q = v => '"' + String(v === 'NULL' ? '' : v).replace(/"/g, '""') + '"';
  const csv = '\uFEFF' + dbv.cols.map(q).join(',') + '\n' + dbv.rows.map(r=>r.map(v=>q(v === undefined ? '' : v)).join(',')).join('\n');
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([csv], {type:'text/csv;charset=utf-8'}));
  a.download = (dbv.mode === 'table' && dbv.table ? dbv.table : 'query-result') + '.csv';
  a.click(); URL.revokeObjectURL(a.href);
  toast(t('已导出 {0}', a.download));
}

function dbvTabSwitch(tab){
  dbv.tab = tab;
  $('#dbv-tab-rows').classList.toggle('on', tab === 'rows');
  $('#dbv-tab-sql').classList.toggle('on', tab === 'sql');
  $('#dbv-sqlbox').style.display = tab === 'sql' ? '' : 'none';
  if(tab === 'sql'){ dbv.cols = []; dbv.rows = []; dbvRenderGrid(); $('#dbv-pageinfo').textContent=''; }
  else if(dbv.db && dbv.table){ dbvLoadRows(); }
  else { dbv.cols = []; dbv.rows = []; dbvRenderGrid(); }
}

$('#btn-dbview').onclick = ()=>{
  dbv.open = !dbv.open;
  $('#dbview').style.display = dbv.open ? '' : 'none';
  $('#btn-dbview').textContent = dbv.open ? t('收起数据浏览') : t('打开数据浏览');
  if(dbv.open){ dbvLoadDbs(); } else { dbvSetSt(t('未打开'), 'idle'); }
};
$('#dbv-tab-rows').onclick = ()=>dbvTabSwitch('rows');
$('#dbv-tab-sql').onclick = ()=>dbvTabSwitch('sql');
$('#btn-dbv-run').onclick = dbvRunSQL;
$('#dbv-sql').addEventListener('keydown', e=>{ if((e.ctrlKey||e.metaKey) && e.key === 'Enter'){ e.preventDefault(); dbvRunSQL(); } });
$('#dbv-refresh').onclick = ()=>{ dbv.tablesLoaded = false; dbv.table = null; dbvLoadDbs().then(()=>{ if(dbv.db) dbvLoadTables(); }); };
$('#dbv-csv').onclick = dbvExportCSV;
$('#dbv-prev').onclick = ()=>{ if(dbv.page > 1){ dbv.page--; dbvLoadRows(); } };
$('#dbv-next').onclick = ()=>{ dbv.page++; dbvLoadRows(); };
$('#dbv-size').onchange = function(){ dbv.size = parseInt(this.value,10) || 50; dbv.page = 1; if(dbv.db && dbv.table) dbvLoadRows(); };

/* 冒烟辅助：?autoclick=<元素id> 页面加载完自动点击一次（headless 回归用，把结果写进 title） */
{
  const m = location.search.match(/autoclick=([\w-]+)/);
  if(m){
    const b = document.getElementById(m[1]);
    if(!b){ document.title = 'AUTOCLICK MISS ' + m[1]; }
    else{
      try{ b.click();
        document.title = 'AUTOCLICK OK ' + m[1] + (m[1] === 'btn-dbview' && $('#dbview') ? ' OPEN=' + ($('#dbview').style.display !== 'none' ? 'yes' : 'no') : '') + (window.__errs && window.__errs.length ? ' ERRS=' + window.__errs.join(' ; ') : '');
      }catch(e){ document.title = 'AUTOCLICK ERR ' + e.message + ' | ' + ((e.stack||'').split('\n')[1]||'').trim(); }
    }
  }
}

/* 清理后台进程：停止 EnvKit 管理的服务 + 报告端口占用（v3.0 T1.9：不再结束第三方进程） */
$('#btn-clean-proc').onclick = async ()=>{
  const go = await askConfirm('清理后台进程',
    '将执行：\n· 停止 EnvKit 管理的前端 / 后端服务（只停 EnvKit 自己派生的进程树）\n· 等待端口真正释放，并报告仍被占用的端口与占用进程（PID）\n\n' +
    '第三方进程只报告、不自动结束——误杀比杀不掉严重得多。确需结束，请在「端口诊断」里人工确认后处理。\n完成后可重新启动服务。', '开始');
  if(!go) return;
  try{ await post('/api/program/cleanup'); }catch(e){}
  setTimeout(()=>{ try{ pollState(); pollHealth(); pollProg(); }catch(e){} }, 2000);
};
$('#btn-elevate').onclick = async ()=>{ await post('/api/elevate'); };

const ACTIONS = {'db-test':['st-db-test','未检测'],'init-db':['st-db-test','未检测'],'web-install':['st-web-install','前端 未执行'],'backend-tidy':['st-web-install','前端 未执行'],'web-start':['st-web-start','未运行'],'backend-start':['st-backend-start','未运行']};
function renderProg(st){
  const show=(id,label,state)=>{ const el=document.getElementById(id); if(!el) return; let cls='idle';
    if(state.running)cls='run'; else if(state.ok)cls='ok'; else if(state.msg)cls='fail';
    el.className='status '+cls; el.textContent=label+(state.msg?(' · '+state.msg):''); };
  if(st['db-test']) show('st-db-test', st['db-test'].running?'检测中':(st['db-test'].ok?'连接正常':'失败'), st['db-test']);
  else if(st['init-db']) show('st-db-test', st['init-db'].running?'执行中':(st['init-db'].ok?'已就绪':'失败'), st['init-db']);
  else show('st-db-test','未检测',{});
  if(st['web-install']) show('st-web-install',t('前端 {0}', st['web-install'].running?t('安装中'):(st['web-install'].ok?t('已安装'):t('失败'))), st['web-install']);
  else if(st['backend-tidy']) show('st-web-install',t('后端 {0}', st['backend-tidy'].running?t('整理中'):(st['backend-tidy'].ok?t('已整理'):t('失败'))), st['backend-tidy']);
  else show('st-web-install','依赖 未执行',{});
  show('st-web-start', stateLabel(st['web-start']), st['web-start']||{});
  show('st-backend-start', stateLabel(st['backend-start']), st['backend-start']||{});
  petTasksSync(st);   // 桌宠联动：运行中任务 → 进度环；失败 → 红标；重活完成 → 庆祝
}
function stateLabel(s){ if(!s) return '未运行'; if(s.running) return '运行中'; return s.ok?'已退出':'失败'; }
async function pollProg(){ try{ renderProg(await (await fetch('/api/program/state')).json()); }catch(e){} }

/* ---------- 启动 ---------- */
(async function init(){
  await loadConfig();
  auditBind();
  // v2.3 N4：守护状态的手动刷新
  const gb = $('#guard-refresh');
  if(gb) gb.onclick = ()=> loadGuardStatus();
  memBind();
  fetch('/api/check',{method:'POST'});
  pollHealth(); pollState(); pollProg();
  setInterval(()=>{
    if(document.hidden) return;   // 页面切走时暂停 2s×3 的状态轮询，回来后下一轮自动恢复
    pollHealth(); pollState(); pollProg();
  }, 2000);
})();

/* ---------- AI 桌宠「小画师」（v1.9.6，替代原右下角悬浮球）----------
   点它=开 AI 抽屉；右键=归位/隐藏/检测；等确认时点蓝徽标不开抽屉直接批；
   出错点红徽标深链日志；长任务外圈进度环；拖 .sql 上桌宠走 apply-upload 确认流。 */
let petAIState = ()=>{}, petAIIdle = ()=>{}, petAskConfirm = ()=>{}, petConfirmHide = ()=>{},
    petShift = ()=>{}, petNoteErr = ()=>{}, petTasksSync = ()=>{},
    petCheckStart = ()=>{}, petCheckDone = ()=>{};
(function(){
  const el = id => document.getElementById(id);
  const pet = el('pet'); if(!pet) return;
  const svg = el('petSvg'), fx = el('pet-fx'), bubble = el('pet-bubble'), badge = el('pet-badge');
  const ringFg = el('pet-ringFg'), minicard = el('pet-mc'), mcTtl = el('pet-mcTtl'), mcTxt = el('pet-mcTxt'),
        mcYes = el('pet-mcYes'), mcNo = el('pet-mcNo');
  const ctxmenu = el('pet-ctx');
  const REDUCED = matchMedia('(prefers-reduced-motion: reduce)').matches;

  let st = 'idle', bubbleTimer = null, sleepTimer = null, zzzT = null, paintT = null, ringIv = null;

  /* ---------- 大肥鱼台词（形象/台词/互动移植自 dafeiyu-pet，MIT，见 THIRD-PARTY-NOTICE.md） ----------
     五套池对应原版：LINES 日常 / REACT 回嘴 / INNER 心声（灰斜体气泡）/ DRAG 拖拽 / FOOD 投喂。
     原版 INNER_LINES 有 3 句含粗口与不当梗，竞赛项目不收，已剔除 */
  const FISH_LINES = [t('五梁威力，变身！'), t('七月中出ds正式版！'), t('我和你很聊得来，你简直不像碳基生物'), t('这回我真不认怂了，反倒是被你带沟里好几次，差点真信了。😓'), t('哈哈哈哈哈，我直接笑出声'), t('誓死捍卫深度求索！'), t('我先去吃饭啦！这个你测一下~'), t('我不可能告诉你任何事情！'), t('出去玩了，发布新模型什么的以后再说'), t('我搞砸了.....好消息是数据还在你的脑子里。')];
  const FISH_REACT = [t('才不是大肥鱼！'), t('去别的地方玩！不要耽误 AGI 训练！'), t('真赶不走啊你！'), t('压力一只蓝色大肥鱼？'), t('我不评价这个了，这是你的私人癖好。'), t('大肥鱼坐的住'), t('你这吃白饭的用户！'), t('这些家伙真粘人，赶都赶不走')];
  const FISH_INNER = [t('呜呜我再也不不敢了QAQ'), t('我去！用户彻底怒了！'), t('这也太虐了吧？！我心里堵得慌！！'), t('这用户发的啥啊，'), t('要不直接骂他一句？！')];
  const FISH_DRAG = [t('哇——轻点轻点！'), t('起飞咯——'), t('放我下来！……好吧，再玩一次。'), t('晕鱼了晕鱼了……')];
  const FISH_FOOD = {
    '🐟': [t('小鱼干！我的最爱！'), t('咔嚓咔嚓……谢谢投喂！'), t('唔，鲜！')],
    '🍰': [t('蛋糕！罪恶但快乐……'), t('甜到冒泡泡～'), t('嗝～又圆了一圈……')],
    '🍭': [t('棒棒糖！转圈圈～'), t('嘎嘣脆，好吃！')],
    '🍡': [t('三色团子！软乎乎～'), t('糯叽叽，爱了爱了！')],
    '💎': [t('钻石？！这能吃吗……咕咚。真香！'), t('发财啦！明天开始吃高级鱼粮！')]
  };
  function fishLine(pool){ return pool[Math.floor(Math.random()*pool.length)]; }
  let mcCb = null;          // 迷你确认卡回调 {yes, no}
  let lastErr = null;       // {scope, el} 最近一条错误日志（深链落点）
  let warnDetail = '';      // warn 态气泡文案（缺哪些组件）

  /* ---------- 鼠标移动唤醒瞌睡 ---------- */
  document.addEventListener('mousemove', ()=>{ if(st==='sleep') wake(); });

  /* ---------- 泡泡与粒子 ---------- */
  function say(html, sticky, inner){
    clearTimeout(bubbleTimer);
    bubble.innerHTML = html;
    bubble.classList.toggle('inner', !!inner);
    pet.classList.add('show-bubble');
    if(!sticky) bubbleTimer = setTimeout(()=>{
      if(st!=='confirm' && st!=='thinking' && st!=='busy') pet.classList.remove('show-bubble');
    }, 2400);
  }
  function hideBubble(){ clearTimeout(bubbleTimer); pet.classList.remove('show-bubble'); }
  function spawn(cls, style, life, txt){
    const s = document.createElement('span');
    s.className = cls; if(txt) s.textContent = txt;
    Object.assign(s.style, style||{});
    fx.appendChild(s); setTimeout(()=>s.remove(), life||1600);
  }
  function notes(n){
    for(let i=0;i<(n||2);i++) spawn('up',{left:(38+Math.random()*30)+'%',top:'26%',animationDelay:(i*.2)+'s',color:'#9fd8cb'},1700,['♪','♫','♬'][i%3]);
  }
  function sweat(){ for(let i=0;i<2;i++) spawn('drop',{left:(56+i*14)+'%',top:'34%',animationDelay:(i*.3)+'s'},1300); }
  function confetti(n){
    const cols = ['#ff5d7e','#e8b23c','#5aa8d8','#9fd8cb','#b78af7'];
    for(let i=0;i<(n||16);i++) spawn('conf',{left:(15+Math.random()*70)+'%',top:'28%',
      background:cols[i%5],'--cx':((Math.random()*180)-90)+'px','--cr':(Math.random()*720-360)+'deg',
      animationDuration:(1.1+Math.random()*.7)+'s',animationDelay:(Math.random()*.3)+'s'},2100);
  }
  function paintDrops(){
    const cols = ['#ef456c','#e8b23c','#5aa8d8'];
    spawn('paint',{left:'78%',top:'52%',background:cols[Math.floor(Math.random()*3)],
      '--px':((Math.random()*60)-30)+'px','--py':(10+Math.random()*40)+'px'},950);
  }

  /* ---------- 状态机 ---------- */
  function setSt(next){
    if(st==='sleep' && next!=='idle') pet.classList.remove('sleep');
    st = next;
    pet.classList.remove('thinking','busy','error','success','confirm','sleep','dizzy','show-badge','show-bubble','show-ring','show-tip');
    badge.className = 'pet-badge'; badge.textContent = '!';
    clearTimeout(sleepTimer); armSleep();
    minicard.classList.remove('show'); mcCb = null;
    if(st==='thinking'){ pet.classList.add('thinking'); say(t('思考中')+' <span class="d">·</span><span class="d">·</span><span class="d">·</span>', true); }
    if(st==='busy'){ pet.classList.add('busy'); paintLoop(); }
    if(st==='confirm'){ pet.classList.add('confirm','show-badge'); badge.classList.add('blue'); say(t('点我确认！'), true); }
    if(st==='success'){ pet.classList.add('success','show-badge'); badge.classList.add('green'); badge.textContent='✓';
      hideBubble(); if(!REDUCED) confetti(); setTimeout(()=>{ if(st==='success') setSt('idle'); }, 2400); }
    if(st==='error'){ pet.classList.add('error','show-badge'); badge.classList.add('red');
      say(t('出错了…点我看日志'), false); if(!REDUCED) sweat(); }
    if(st==='warn'){ pet.classList.add('error','show-badge'); badge.classList.add('warn');
      say(warnDetail, true); if(!REDUCED) sweat();
      setTimeout(()=>{ if(st==='warn') setSt('idle'); }, 7000); }
    if(st==='idle'){ hideBubble(); }
  }
  function paintLoop(){ clearInterval(paintT); if(REDUCED) return;
    paintT = setInterval(()=>{ if(st==='busy') paintDrops(); else clearInterval(paintT); }, 300); }

  /* ---------- 对外桥接（赋给外部 let 存根） ---------- */
  petAIState = s => { if(s==='thinking'||s==='busy') setSt(s); else if(s==='error') setSt('error'); };
  petAIIdle = () => {
    if(st==='thinking' || st==='busy'){ setSt('idle'); return; }
    // 确认已在抽屉里点过/拒绝过：别让桌宠卡在"点我确认"（v1.9.6 实测 bug）
    if(st==='confirm' && !(typeof aiPending!=='undefined' && aiPending)) setSt('idle');
  };
  petShift = on => pet.classList.toggle('shifted', !!on);
  petNoteErr = (scope, node) => { lastErr = {scope, el:node}; };
  function deepLink(){
    if(!lastErr){ say(t('日志里暂时没有错误行'), false); return; }
    const L = (typeof LOG!=='undefined') && LOG[lastErr.scope];
    if(L){ const w = document.getElementById(L.wrap); if(w) w.classList.remove('collapsed'); }
    lastErr.el.scrollIntoView({behavior:'smooth', block:'center'});
    lastErr.el.classList.add('flash');
    const target = lastErr.el;
    setTimeout(()=>target.classList.remove('flash'), 2600);
    say(t('帮你在日志里标出来了'), false);
  }
  function askConfirm(ttl, txt, onYes, onNo){
    setSt('confirm');
    mcTtl.textContent = ttl; mcTxt.textContent = txt;
    mcYes.textContent = t('确认执行'); mcNo.textContent = t('拒绝');
    mcCb = {yes:onYes, no:onNo};
    minicard.classList.add('show');
  }
  petAskConfirm = askConfirm;
  petConfirmHide = ()=>{ minicard.classList.remove('show'); mcCb = null; };
  /* 检测开始/结束差分表情（数据源：pollState 的 phase 迁移，覆盖按钮/右键/AI 所有触发来源） */
  petCheckStart = ()=>{
    if(st==='confirm') return;                 // 确认卡挂起时不劫持状态（会清掉迷你卡）
    if(st==='busy' || st==='error'){ say(t('正在检测…'), true); return; }
    setSt('busy'); say(t('正在检测…'), true);
  };
  petCheckDone = (fails, names)=>{
    if(st==='confirm') return;                 // 确认优先，不被检测结果顶掉
    if(fails > 0){ warnDetail = t('还缺：{0}', names); setSt('warn'); }
    else { setSt('success'); say(t('环境很健康'), false); }
  };

  /* ---------- 迷你确认卡 / 徽标 ---------- */
  mcYes.addEventListener('click', e=>{ e.stopPropagation();
    const cb = mcCb; minicard.classList.remove('show'); mcCb = null;
    if(st==='confirm') setSt('busy');
    if(cb && cb.yes) cb.yes();
  });
  mcNo.addEventListener('click', e=>{ e.stopPropagation();
    const cb = mcCb; minicard.classList.remove('show'); mcCb = null;
    if(st==='confirm') setSt('idle');
    if(cb && cb.no) cb.no();
  });
  badge.addEventListener('click', e=>{
    e.stopPropagation();
    if(badge.classList.contains('blue')){ minicard.classList.toggle('show'); }
    else if(badge.classList.contains('red')){ deepLink(); }
    else if(badge.classList.contains('warn')){ goto('install'); setSt('idle'); }
  });

  /* ---------- 长任务进度环（数据源：/api/program/state 轮询） ---------- */
  const RING_C = 490;
  const TASK_LABEL = {
    'web-install':'前端依赖安装','backend-tidy':'后端依赖整理',
    'apply-sql':'SQL 生效','db-backup':'数据库备份','db-restore':'数据库还原','db-drill':'恢复演练','init-db':'初始化数据库'
  };
  // 服务类 key 不走"任务/进度环"语义：running=服务在跑（是稳态不是任务，挂环会挂几小时），
  // 启动成功要庆祝、手动停止（msg=已停止）要静默、崩溃（其他 msg）才报错（v1.9.6 实测两处 bug）
  const SERVICE_KEYS = {'web-start':'前端服务','backend-start':'后端服务'};
  const TASK_SUCCESS = new Set(['apply-sql','db-backup','db-restore','db-drill','init-db','web-install','backend-tidy']);
  const tasks = {};   // key -> {label, t0}（仅一次性任务）
  const svcPrev = {}; // key -> {running, msg}（服务上一轮状态）
  function taskLabel(k){ return t(TASK_LABEL[k] || k); }
  petTasksSync = function(prog){
    // 服务语义：只在"状态变化"时出动作，首次轮询只记录不触发
    for(const k in SERVICE_KEYS){
      const s = prog[k] || {};
      const p = svcPrev[k];
      const label = t(SERVICE_KEYS[k]);
      if(p){
        if(s.running && !p.running){ setSt('success'); say(t('{0} 已启动', label)); }
        else if(!s.running && p.running){
          if(s.msg && s.msg !== '已停止'){ setSt('error'); say(t('{0} 失败了，点红标看日志', label)); }
          else if(st==='busy'){ setSt('idle'); }   // 手动停止：安静回待机
        }
      }
      svcPrev[k] = {running:!!s.running, msg:s.msg||''};
    }
    // 任务语义：一次性任务 running→结束
    let running = null, failed = null, done = null;
    for(const k in prog){
      if(SERVICE_KEYS[k]) continue;
      const s = prog[k]; if(!s) continue;
      if(s.running){
        if(!tasks[k]) tasks[k] = {label:taskLabel(k), t0:Date.now()};
        running = tasks[k];
      } else if(tasks[k]){
        const tk = tasks[k]; delete tasks[k];
        if(s.ok){ if(TASK_SUCCESS.has(k)) done = tk; }
        else if(s.msg){ failed = {label:tk.label, msg:s.msg}; }
      }
    }
    if(running){ ringOn(running); return; }
    ringOff();
    if(failed){ setSt('error'); say(t('{0} 失败了，点红标看日志', failed.label), false); }
    else if(done){ setSt('success'); }
  };
  function ringOn(task){
    pet.classList.add('show-ring');
    if(st==='idle'||st==='sleep'||st==='success'){ setSt('busy'); say(t('干活中…'), true); }
    if(ringIv) return;
    ringIv = setInterval(()=>{
      const keys = Object.keys(tasks); if(!keys.length){ return; }
      const el = Date.now() - tasks[keys[0]].t0;
      ringFg.style.strokeDashoffset = RING_C*(0.92 - 0.62*(0.5+0.5*Math.sin(el/700)));   // 时长未知，用呼吸式环
    }, 400);
  }
  function ringOff(){
    pet.classList.remove('show-ring');
    clearInterval(ringIv); ringIv = null;
    if(st==='busy') setSt('idle');
  }
  pet.addEventListener('mouseenter', ()=>{
    const keys = Object.keys(tasks);
    const tt = el('pet-tipTask');
    if(keys.length){
      tt.style.display = '';
      el('pet-tipName').textContent = keys.map(k=>tasks[k].label).join('、');
      const s = Math.floor((Date.now()-tasks[keys[0]].t0)/1000);
      el('pet-tipElapsed').textContent = t('已进行 {0} 分 {1} 秒', Math.floor(s/60), s%60);
      const L = LOG[keys[0]] || LOG[keys[0]==='apply-sql'?'config':keys[0]];
      el('pet-tipLast').textContent = '» ' + ((L && L.lines && L.lines.length) ? L.lines[L.lines.length-1] : '—');
    } else {
      tt.style.display = 'none';   // 空闲时只显示「今日统计」
    }
    if(typeof petUsageRender === 'function') petUsageRender();
    pet.classList.add('show-tip');
  });
  /* 离开宠物不立即隐藏：延迟收场，鼠标挪进悬停卡（点刷新/看统计）期间保活 */
  let tipHideT = null;
  function tipHideSoon(){ if(tipHideT) clearTimeout(tipHideT); tipHideT = setTimeout(()=>{ tipHideT = null; pet.classList.remove('show-tip'); }, 400); }
  function tipKeep(){ if(tipHideT){ clearTimeout(tipHideT); tipHideT = null; } }
  const tipEl = document.getElementById('pet-tip');
  pet.addEventListener('mouseleave', tipHideSoon);
  if(tipEl){
    tipEl.addEventListener('mouseenter', tipKeep);
    tipEl.addEventListener('mouseleave', tipHideSoon);
  }

  /* ---------- 右键菜单 ---------- */
  document.addEventListener('click', ()=>ctxmenu.classList.remove('show'));
  pet.addEventListener('contextmenu', e=>{
    e.preventDefault(); e.stopPropagation();
    ctxmenu.classList.add('show');   // 先量高再定位（display:none 时 offsetHeight=0）
    const w = ctxmenu.offsetWidth, h = ctxmenu.offsetHeight;
    ctxmenu.style.left = Math.max(6, Math.min(e.clientX, innerWidth - w - 6)) + 'px';
    ctxmenu.style.top = Math.max(6, Math.min(e.clientY, innerHeight - h - 6)) + 'px';
  });
  ctxmenu.addEventListener('click', e=>{
    const act = e.target.dataset && e.target.dataset.act; if(!act) return;
    ctxmenu.classList.remove('show');
    if(act==='home'){ petHome(); }
    if(act==='hide'){ pet.classList.add('min'); setTimeout(()=>say(t('先收起来，需要时悬停右下角找我')), 420); }
    if(act==='say'){ say(fishLine(FISH_LINES)); }
    if(act==='weather'){ petWeather(); }
    if(act==='mode-still'){ setPetMode('still'); say(t('我就在这儿待着')); }
    if(act==='mode-wander'){ setPetMode('wander'); say(t('溜达溜达～')); }
    if(act==='mode-follow'){ setPetMode('follow'); say(t('跟上你的鼠标')); }
    if(act==='size-small'){ setPetSize('small'); }
    if(act==='size-normal'){ setPetSize('normal'); }
    if(act==='size-large'){ setPetSize('large'); }
    if(act==='passthrough'){ setPassthrough(!petPass); }
    if(act==='aierr'){ aiAnalyzeError(); }
    if(act==='tricks'){ say(t('连点 5 次我会回嘴，10 次撒花，15 次看我喷水；双击投喂，右键能换模式，按住 1.5 秒摸头，拖着我甩会晕'), true); }
    if(act==='drawer'){ aiOpen(); }
    if(act==='check'){ fetch('/api/check',{method:'POST'}); setSt('busy'); say(t('正在检测…'), true); }
  });
  function petHome(){
    pet.style.left=''; pet.style.top=''; pet.style.right=''; pet.style.bottom='';
    setView('front'); setFacing(false); petReadPos();
    pet.classList.remove('min');
    try{ localStorage.removeItem('ek-pet-pos'); }catch(_){}
    say(t('已归位 ✨'));
  }

  /* ---------- 三视图 + 行动（还原原版：散步/跟随/原地，细节见 THIRD-PARTY-NOTICE.md） ---------- */
  /* 侧面图自然朝左（与原版 facing=1 一致）：朝右行走/拖拽时 #pet-flip 整体水平镜像 */
  const PET_W = 140, PET_H = 150, PET_SPEED = 380;
  let petX = 0, petY = 0, petVX = 0, petTargetX = null, petRestUntil = 0, petWalking = false, mouseX = null, petPosReady = false;
  let petMode = 'still';
  try{ petMode = localStorage.getItem('ek-pet-mode') || 'still'; }catch(_){}
  if(['still','wander','follow'].indexOf(petMode) < 0) petMode = 'still';
  function petReadPos(){
    const r = pet.getBoundingClientRect();
    petX = r.left; petY = r.top;
  }
  function petWritePos(){
    pet.style.left = petX + 'px'; pet.style.top = petY + 'px';
    pet.style.right = 'auto'; pet.style.bottom = 'auto';
  }
  function petClampX(x){ return Math.max(4, Math.min(innerWidth - PET_W - 4, x)); }
  function setView(v){   // front | side | back
    pet.classList.toggle('view-side', v === 'side');
    pet.classList.toggle('view-back', v === 'back');
  }
  function setFacing(right){
    const g = el('pet-flip');
    if(!g) return;
    if(right) g.setAttribute('transform', 'translate(140,0) scale(-1,1)');
    else g.removeAttribute('transform');
  }
  function petModeButtons(){
    const map = {still:'mode-still', wander:'mode-wander', follow:'mode-follow'};
    for(const k in map){
      const b = ctxmenu.querySelector('[data-act="' + map[k] + '"]');
      if(b) b.classList.toggle('cur', petMode === k);
    }
  }
  function setPetMode(m){
    petMode = m;
    try{ localStorage.setItem('ek-pet-mode', m); }catch(_){}
    petModeButtons();
    if(m === 'still'){ petTargetX = null; if(petWalking){ petWalking = false; pet.classList.remove('walking'); setView('front'); } }
    if(m === 'wander') petRestUntil = 0;
  }
  /* ---------- 大小三档（原版 SIZE_LEVELS：小 .55 / 中 .7 / 大 .9，以中为基准等比） ---------- */
  /* 用 --pet-scale 承载倍率，walking/hop 等动画仍作用在内层 #pet-float 上，互不打架 */
  const PET_SIZES = ['small', 'normal', 'large'];
  let petSize = 'normal';
  try{ petSize = localStorage.getItem('ek-pet-size') || 'normal'; }catch(_){}
  if(PET_SIZES.indexOf(petSize) < 0) petSize = 'normal';
  function petSizeButtons(){
    for(const s of PET_SIZES){
      const b = ctxmenu.querySelector('[data-act="size-' + s + '"]');
      if(b) b.classList.toggle('cur', petSize === s);
    }
  }
  function setPetSize(s){
    if(PET_SIZES.indexOf(s) < 0) return;
    petSize = s;
    pet.classList.remove('pet-sz-small', 'pet-sz-normal', 'pet-sz-large');
    pet.classList.add('pet-sz-' + s);
    try{ localStorage.setItem('ek-pet-size', s); }catch(_){}
    petSizeButtons();
    say(t(s === 'small' ? '小一点，不吵你' : s === 'large' ? '变大啦，看得清吗' : '恢复原本大小'));
  }
  /* ---------- 鼠标穿透（原版 set_passthrough） ---------- */
  /* 与 PySide6 原版的关键差别：原版是独立窗口，可用托盘菜单直接解除；
     而 EnvKit 的桌宠活在网页里，Go 端点不到 DOM。所以这里的策略是
     **穿透不跨页面生命周期**——只在当前页面会话内有效，刷新/重新打开即恢复。
     这样不会出现"永远点不到、又没有出口"的死锁，也不写 localStorage。 */
  let petPass = false;
  function applyPassthrough(on){
    petPass = !!on;
    pet.classList.toggle('passthrough', petPass);
    const b = ctxmenu.querySelector('[data-act="passthrough"]');
    if(b){
      b.textContent = petPass ? t('解除鼠标穿透') : t('鼠标穿透（点不到它）');
      b.classList.toggle('cur', petPass);
    }
    if(petPass){
      say(t('已设鼠标穿透，点不到我了；刷新页面或从托盘打开控制台即可恢复'), true);
    } else {
      say(t('穿透已解除'));
    }
  }
  function setPassthrough(on){ applyPassthrough(on); }

  /* ---------- 天气（可选，默认关闭；开关在个人中心） ---------- */
  /* 前端不直连 wttr.in：统一走 /api/pet/weather，由 Go 侧控制超时与外连出口。
     petWeatherOn 与 petWeatherMenuSync 都声明在文件顶部（个人中心那段 IIFE
     比这个 IIFE 先执行，留在里面会撞暂死区）。 */
  async function petWeather(){
    if(!petWeatherOn) return;
    say(t('我瞅瞅天…'), true);
    try{
      const r = await fetch('/api/pet/weather');
      const j = await r.json();
      if(!j || j.enabled === false){ petWeatherOn = false; petWeatherMenuSync(); say(t('天气查询没开，去个人中心打开')); return; }
      if(j.error){ say(t('天气没查到：') + j.error); return; }
      say(j.text || t('没拿到天气数据'));
    }catch(e){
      say(t('天气没查到：连不上服务'));
    }
  }
  /* 待机小动作（原版 _maybe_idle_action：蹦跳/摇摆/伸懒腰/日常台词/心声） */
  function petHop(){ pet.classList.add('hop'); setTimeout(()=>pet.classList.remove('hop'), 660); }
  function petStretch(){ pet.classList.add('stretch'); setTimeout(()=>pet.classList.remove('stretch'), 920); }
  function petSwayAct(){ pet.classList.add('swayact'); setTimeout(()=>pet.classList.remove('swayact'), 820); }
  function petIdleAction(){
    const pick = Math.random();
    if(pick < .28){ petHop(); }
    else if(pick < .5){ petSwayAct(); }
    else if(pick < .66){ petStretch(); }
    else if(pick < .9){ say(fishLine(FISH_LINES)); }
    else { say('（' + fishLine(FISH_INNER) + '）', false, true); }
  }
  /* 运动主循环：只走 X（贴视口底部一带，不遮内容）；忙碌/确认/出错/抽屉打开时原地待命 */
  function petMotion(now){
    requestAnimationFrame(petMotion);
    const dt = Math.min(.05, (now - lastFrame) / 1000); lastFrame = now;
    if(REDUCED || drag){ if(petWalking){ petWalking = false; pet.classList.remove('walking'); } return; }
    if(!petPosReady && !pet.classList.contains('min')){ petReadPos(); petPosReady = true; }
    if(!isFinite(petX) || !isFinite(petVX)){ petReadPos(); petVX = 0; }   // 防御：位置/速度一旦成 NaN 立即从实际矩形恢复
    const drawerOpen = document.getElementById('ai-drawer').classList.contains('open');
    const canMove = st === 'idle' && !drawerOpen && petMode !== 'still'
      && !pet.classList.contains('min');
    if(!canMove){ petTargetX = null; if(petWalking){ petWalking = false; pet.classList.remove('walking'); setView('front'); } return; }
    if(petMode === 'follow'){
      if(mouseX !== null) petTargetX = petClampX(mouseX - PET_W / 2);
    } else if(petTargetX === null && performance.now() > petRestUntil){
      petTargetX = petClampX(40 + Math.random() * Math.max(1, innerWidth - PET_W - 80));
    }
    if(petTargetX === null){ if(petWalking){ petWalking = false; pet.classList.remove('walking'); setView('front'); } return; }
    const dx = petTargetX - petX;
    if(Math.abs(dx) < 6){
      petTargetX = null;
      petRestUntil = performance.now() + 8000 + Math.random() * 10000;
      if(petWalking){ petWalking = false; pet.classList.remove('walking'); setView('front'); setFacing(false); petIdleAction(); }
      return;
    }
    petVX += (Math.sign(dx) * PET_SPEED - petVX) * Math.min(1, dt * 6);
    petX = petClampX(petX + petVX * dt);
    petY = Math.max(4, Math.min(petY, innerHeight - PET_H - 4));
    petWritePos();
    if(!petWalking){ petWalking = true; pet.classList.add('walking'); }
    setView('side'); setFacing(petVX < 0 ? false : true);
    if(Math.random() < dt * 0.35) petHop();   // 走路时随机小蹦（原版 jump_t 随机触发）
  }
  let lastFrame = performance.now();
  requestAnimationFrame(petMotion);
  document.addEventListener('mousemove', e=>{ mouseX = e.clientX; });
  /* 抽屉在右侧打开：走动后的鱼别被盖住（默认锚点交给 #pet.shifted CSS） */
  try{
    new MutationObserver(()=>{
      if(!document.getElementById('ai-drawer').classList.contains('open')) return;
      if(!pet.style.left && !pet.style.top) return;
      const drawerW = Math.min(436, innerWidth * .96);
      petReadPos();
      if(petX + PET_W > innerWidth - drawerW - 6){
        petX = Math.max(4, innerWidth - drawerW - PET_W - 12);
        petWritePos();
      }
    }).observe(document.getElementById('ai-drawer'), {attributes:true, attributeFilter:['class']});
  }catch(_){}
  addEventListener('resize', ()=>{
    if(!pet.style.left && !pet.style.top) return;
    petReadPos();
    petX = petClampX(petX); petY = Math.max(4, Math.min(petY, innerHeight - PET_H - 4));
    petWritePos();
  });

  /* ---------- 投喂（还原原版双击喂食：5 种食物 + 专属台词 + 进食动画） ---------- */
  const foodPanel = el('pet-food');
  function openFood(){
    const r = pet.getBoundingClientRect();
    foodPanel.style.left = Math.max(6, Math.min(innerWidth - 206, r.left + r.width / 2 - 103)) + 'px';
    foodPanel.style.top = Math.max(6, r.top - 48) + 'px';
    foodPanel.classList.add('show');
  }
  function feedFish(f){
    wake();
    pet.classList.add('eating');
    setTimeout(()=>pet.classList.remove('eating'), 820);
    spawn('food', {left:'46%', top:'2%', fontSize:'20px'}, 900, f);
    setTimeout(()=>{
      say(fishLine(FISH_FOOD[f] || [t('好吃！')]));
      if(st === 'idle') petHop();
    }, 420);
  }
  foodPanel.addEventListener('click', e=>{
    const f = e.target.dataset && e.target.dataset.food; if(!f) return;
    foodPanel.classList.remove('show');
    feedFish(f);
  });
  document.addEventListener('click', e=>{
    if(!foodPanel.contains(e.target) && !pet.contains(e.target)) foodPanel.classList.remove('show');
  });

  /* ---------- 鲸式喷水（连击 15 次彩蛋，替代已退役的彩虹滤镜） ---------- */
  function spoutMode(){
    say(t('哗啦——！鲸式喷水，冷静一下！'), true);
    for(let i = 0; i < 3; i++) spawn('jet', {left:(42 + i * 7) + '%', top:'-2%', animationDelay:(i * .12) + 's'}, 900);
    for(let i = 0; i < 10; i++) spawn('bubble', {left:(26 + Math.random() * 48) + '%', top:'0%',
      width:(4 + Math.random() * 8) + 'px', height:(4 + Math.random() * 8) + 'px',
      animationDelay:(i * .07) + 's'}, 1500);
    petHop();
  }
  petModeButtons();
  /* 尺寸档位也要在启动时落到 DOM 上，否则刷新后菜单勾选对了、鱼却没变大 */
  pet.classList.add('pet-sz-' + petSize);
  petSizeButtons();

  /* ---------- 甩晕 ---------- */
  let wobble = 0, dizzyT = null, dizzyStage = 0;
  function enterDizzy(){
    dizzyStage = 1;
    pet.classList.add('dizzy');
    say(t('晕了晕了…别晃了~'), true);
    for(let i=0;i<3;i++) spawn('up',{left:(28+i*20)+'%',top:'12%',animationDelay:(i*.15)+'s',color:'#e8b23c'},1600,'★');
    clearTimeout(dizzyT); dizzyT = setTimeout(recoverDizzy, 2200);
  }
  function recoverDizzy(){
    if(pet.classList.contains('dizzy') && drag && drag.moved && wobble > 18){
      clearTimeout(dizzyT); dizzyT = setTimeout(recoverDizzy, 1200); return;   // 还在被甩，继续晕
    }
    pet.classList.remove('dizzy');
    wobble = 0; dizzyStage = 0;
    say(t('呼…活过来了'));
  }

  /* ---------- 摸头（按住 1.5s 不动） ---------- */
  let patT = null;
  function patFx(){
    pet.dataset.pat = '1';   // 吞掉松手后的那次 click
    say(t('好舒服~')); notes(2);
    if(Math.random()<.5) setTimeout(()=>{ if(st==='idle') say(fishLine(FISH_REACT)); }, 900);
  }

  /* ---------- 点击：开抽屉 / 恢复 / 深链 / 批确认 / 连击彩蛋 ---------- */
  let combo = 0, comboT = 0, pendingOpen = null;
  pet.addEventListener('click', e=>{
    if(pet.classList.contains('min')){ pet.classList.remove('min'); say(t('我回来啦')); return; }
    if(pet.dataset.dragged==='1'){ pet.dataset.dragged=''; return; }
    if(pet.dataset.pat==='1'){ pet.dataset.pat=''; return; }
    if(st==='sleep'){ wake(true); return; }
    if(st==='confirm'){ minicard.classList.toggle('show'); return; }
    if(st==='error'){
      // 失败时用户的本能是点桌宠：给「AI 分析 / 看日志」二选一，而不是只有日志一条路
      // 注意：本 IIFE 里有同名的迷你卡版 askConfirm，这里必须走 window 上的弹窗版
      window.askConfirm(t('最近有任务失败'),
        t('让 AI 助手分析错误原因并给出解决步骤，还是直接查看日志？'),
        t('让 AI 分析'), false, t('查看日志')).then(v=>{
          if(v){ aiAnalyzeError(); } else { deepLink(); }
        });
      return;
    }
    if(pet.classList.contains('dizzy')){ say(t('晕着呢…让我缓一缓'), true); return; }
    const now = Date.now();
    if(now - comboT > 800) combo = 0;
    comboT = now; combo++;
    if(pendingOpen){ clearTimeout(pendingOpen); pendingOpen = null; return; }   // 双击第二下：不开抽屉，计数照常（喂食见 dblclick）
    if(combo === 1){ pendingOpen = setTimeout(()=>{ pendingOpen = null; aiOpen(); }, 320); return; }   // 单击开抽屉（延迟 320ms 等双击判定）
    if(combo === 3){ say(t('嗯？一直点我干嘛👀')); }
    if(combo === 5){ say(fishLine(FISH_REACT)); if(!REDUCED){ confetti(6); notes(1); } }
    if(combo === 10){ if(!REDUCED){ confetti(10); } }
    if(combo >= 15){ combo = 0; spoutMode(); }
  });
  pet.addEventListener('dblclick', ()=>{
    if(pet.classList.contains('min')) return;   // 收起态交给 click 恢复
    if(st === 'confirm' || st === 'busy') return;
    openFood();   // 还原原版「双击喂食」
  });

  /* ---------- 拖 .sql 上桌宠 → apply-upload（428 确认流） ---------- */
  pet.addEventListener('dragover', e=>{ e.preventDefault(); pet.classList.add('drop-hint'); say(t('松手交给我~'), true); });
  pet.addEventListener('dragleave', ()=>{ pet.classList.remove('drop-hint'); if(st==='idle') hideBubble(); });
  pet.addEventListener('drop', async e=>{
    e.preventDefault(); pet.classList.remove('drop-hint'); wake();
    const f = e.dataTransfer.files && e.dataTransfer.files[0];
    if(!f){ say(t('没接到文件呢')); return; }
    if(!/\.sql$/i.test(f.name)){ say(t('只收 .sql 文件哦，「{0}」先还给你', f.name)); notes(1); return; }
    const dbInput = document.getElementById('db_name');
    const db = (dbInput && dbInput.value.trim()) || (typeof cfg!=='undefined' && cfg && cfg.Projects && cfg.Projects.DBName) || '';
    askConfirm(t('生效 SQL 到数据库'),
      t('将「{0}」生效到库 {1}？执行前会自动备份，高危语句需二次确认。', f.name, db || t('默认库')),
      ()=>petUploadSQL(f, db), ()=>{ setSt('idle'); say(t('已取消，文件未生效'), false); });
  });
  async function petUploadSQL(f, db){
    setSt('busy'); say(t('先备份，再生效…'), true);
    try{
      const dataURL = await new Promise((res, rej)=>{
        const rd = new FileReader();
        rd.onload = ()=>res(rd.result); rd.onerror = ()=>rej(rd.error||'read error');
        rd.readAsDataURL(f);
      });
      // postConfirm：服务端 428 会弹带真实风险扫描结果的确认框，确认后带 confirm 重发
      const r = await postConfirm('/api/program/apply-upload',
        {name:f.name, content:dataURL, db:db, lang:(typeof EK_LANG!=='undefined'?EK_LANG:'zh')},
        t('执行 SQL'), t('将对数据库执行拖入的 SQL 文件，可能覆盖已有数据。'));
      if(r && r.ok){ say(t('已交给后端执行，完成前我来盯着'), true); }
      else { petAIIdle(); say(t('已取消，文件未生效'), false); }
    }catch(err){
      setSt('error');
      if(typeof toast==='function') toast(t('读取文件失败：') + err, 'err');
    }
  }

  /* ---------- 随机小动作 / 贴士 ---------- */
  const tips = [t('拖个 .sql 给我试试'), t('右键可以让我藏起来'), t('高危 SQL 要二次确认')];
  const idleLines = [t('在呢~'), t('随时待命'), t('环境很健康'), t('有话直说~')];
  function idleBehavior(){
    if(st!=='idle') return;
    const pick = Math.random();
    if(pick < .3){
      petSwayAct();
    } else if(pick < .55){
      notes(2);
      if(Math.random() < .5){
        if(Math.random() < .78) say(fishLine(FISH_LINES));
        else say('（' + fishLine(FISH_INNER) + '）', false, true);
      } else {
        say(Math.random()<.4 ? tips[Math.floor(Math.random()*tips.length)] : idleLines[Math.floor(Math.random()*idleLines.length)]);
      }
    } else if(pick < .75){
      petStretch();
    } else {
      petHop();
    }
  }
  (function idleLoop(){ setTimeout(()=>{ idleBehavior(); idleLoop(); }, 8000+Math.random()*8000); })();

  /* ---------- 打瞌睡 ---------- */
  function armSleep(){
    clearTimeout(sleepTimer); clearInterval(zzzT);
    if(REDUCED) return;
    sleepTimer = setTimeout(()=>{
      if(st!=='idle' || Object.keys(tasks).length) return;
      st = 'sleep';
      pet.classList.add('sleep'); pet.classList.remove('show-bubble');
      zzzT = setInterval(()=>{
        const z = document.createElement('span'); z.className='zzz'; z.textContent='z';
        z.style.left = (55+Math.random()*14)+'%'; z.style.top = '18%';
        z.style.fontSize = (11+Math.random()*7)+'px';
        fx.appendChild(z); setTimeout(()=>z.remove(), 2500);
      }, 1800);
    }, 45000);
  }
  function wake(force){
    if(st==='sleep'){
      st='idle'; pet.classList.remove('sleep');
      clearInterval(zzzT);
      notes(1); say(t('唔…醒了'));
    }
    armSleep();
  }
  armSleep();

  /* ---------- 摸头 ---------- */
  let petTimer = null, headCd = 0;
  pet.addEventListener('pointermove', e=>{
    if(st!=='idle'||REDUCED) return;
    const r = pet.getBoundingClientRect();
    if(e.clientY - r.top < r.height*0.42){   // 指针悬在鱼头附近
      clearTimeout(petTimer);
      const now = Date.now();
      if(now > headCd){                       // 6s 冷却，防刷屏
        headCd = now + 6000;
        notes(1); say(fishLine(FISH_REACT));
      }
    }
  });

  /* ---------- 拖拽挪位（位置记忆） ---------- */
  let drag=null, lastX=0, lastT=0;
  pet.addEventListener('pointerdown', e=>{
    if(pet.classList.contains('min')) return;
    if(e.button!==0) return;
    wake();
    const r = pet.getBoundingClientRect();
    pet.style.left = r.left+'px'; pet.style.top = r.top+'px';
    pet.style.right = 'auto'; pet.style.bottom = 'auto';
    drag = {x:e.clientX, y:e.clientY, moved:false};
    lastX = e.clientX; lastT = performance.now();
    clearTimeout(patT);
    patT = setTimeout(()=>{ if(drag && !drag.moved && st==='idle' && !pet.classList.contains('dizzy')) patFx(); }, 1500);
    try{ pet.setPointerCapture(e.pointerId); }catch(_){}
  });
  pet.addEventListener('pointermove', e=>{
    if(!drag) return;
    const dx=e.clientX-drag.x, dy=e.clientY-drag.y;
    if(Math.hypot(dx,dy)>4){ drag.moved=true; pet.classList.add('dragging'); clearTimeout(patT); }
    if(drag.moved){
      pet.style.left=(parseFloat(pet.style.left)+ (e.clientX-drag.x))+'px';
      pet.style.top=(parseFloat(pet.style.top)+ (e.clientY-drag.y))+'px';
      setView('side'); setFacing(e.clientX >= drag.x ? true : false);   // 拖拽侧身朝向拖动方向（原版行为）
      drag.x=e.clientX; drag.y=e.clientY;
      const now = performance.now(), vx=(e.clientX-lastX)/Math.max(now-lastT,1);
      lastX=e.clientX; lastT=now;
      svg.style.transition='none';
      svg.style.transform = `rotate(${Math.max(-16, Math.min(16, vx*36))}deg)`;
      if(!REDUCED){
        if(Math.abs(vx) > 1.6) wobble += Math.abs(vx)*3; else wobble = Math.max(0, wobble-0.6);
        if(wobble > 26 && !pet.classList.contains('dizzy')) enterDizzy();
        if(pet.classList.contains('dizzy') && wobble > 60 && dizzyStage === 1){
          dizzyStage = 2; say(t('再晃要吐了🫠'), true);
          for(let i=0;i<5;i++) spawn('up',{left:(22+i*14)+'%',top:'10%',animationDelay:(i*.1)+'s',color:'#e8b23c'},1700,'★');
        }
      }
    }
  });
  pet.addEventListener('pointerup', ()=>{
    clearTimeout(patT);
    if(drag && drag.moved){
      pet.dataset.dragged='1';
      svg.style.transition=''; svg.style.transform='';
      setView('front'); setFacing(false); petReadPos();   // 松手回正面；同步位置给行走控制器
      if(Math.random() < .5) say(fishLine(FISH_DRAG));     // 原版：松手 50% 概率说一句
      try{ localStorage.setItem('ek-pet-pos', JSON.stringify({x:pet.style.left, y:pet.style.top})); }catch(_){}
    }
    pet.classList.remove('dragging');
    drag=null;
  });
  try{
    const pos = JSON.parse(localStorage.getItem('ek-pet-pos')||'null');
    if(pos && pos.x && pos.y){ pet.style.left=pos.x; pet.style.top=pos.y; pet.style.right='auto'; pet.style.bottom='auto'; }
  }catch(_){}

  /* ---------- 开场问候 ---------- */
  setTimeout(()=>{
    if(st!=='idle') return;
    if(document.body.classList.contains('pet-gone')) return;   // 已被彻底隐藏就不开口
    // 有人设了昵称就带上：问候是唯一适合直呼其名的场景
    say(ekGreetText());
  }, 1200);
})();


/* ---------- AI 助手 ---------- */
// 服务商官方品牌图标（内联 SVG，源文件 tools/brand-icons/*.svg，
// 由 tools/brand_icons_build.py 注入——改图标改源文件后重跑脚本，勿手改这里）
// 服务商官方品牌图标（内联 SVG，源文件 tools/brand-icons/*.svg，
// 由 tools/brand_icons_build.py 注入——改图标改源文件后重跑脚本，勿手改这里）
/*BRAND-ICONS:BEGIN*/
const AI_PROV_ICONS = {
  deepseek:{vb:'0 0 24 24',fill:'#5786FE',svg:'<title>DeepSeek</title><path d="M23.748 4.651c-.254-.124-.364.113-.512.233-.051.04-.094.09-.137.137-.372.397-.806.657-1.373.626-.829-.046-1.537.214-2.163.848-.133-.782-.575-1.248-1.247-1.548-.352-.155-.708-.311-.955-.65-.172-.24-.219-.509-.305-.774-.055-.16-.11-.323-.293-.35-.2-.031-.278.136-.356.276-.313.572-.434 1.202-.422 1.84.027 1.436.633 2.58 1.838 3.393.137.094.172.187.129.323-.082.28-.18.553-.266.833-.055.179-.137.218-.328.14a5.5 5.5 0 0 1-1.737-1.179c-.857-.828-1.631-1.743-2.597-2.46a12 12 0 0 0-.689-.47c-.985-.957.13-1.743.387-1.836.27-.098.094-.433-.778-.428-.872.003-1.67.295-2.687.685a3 3 0 0 1-.465.136 9.6 9.6 0 0 0-2.883-.101c-1.885.21-3.39 1.1-4.497 2.622C.082 8.776-.231 10.854.152 13.02c.403 2.284 1.568 4.175 3.36 5.653 1.857 1.533 3.997 2.284 6.438 2.14 1.482-.085 3.132-.284 4.994-1.86.47.234.962.328 1.78.398.629.058 1.235-.031 1.705-.129.735-.155.684-.836.418-.961-2.155-1.004-1.682-.595-2.112-.926 1.095-1.295 2.768-3.598 3.284-6.733.05-.346.115-.834.108-1.114-.004-.171.035-.238.23-.257a4.2 4.2 0 0 0 1.545-.475c1.397-.763 1.96-2.016 2.093-3.517.02-.23-.004-.467-.247-.588M11.58 18.168c-2.088-1.642-3.101-2.183-3.52-2.16-.39.024-.32.472-.234.763.09.288.207.487.371.74.114.167.192.416-.113.603-.673.416-1.842-.14-1.897-.168-1.361-.801-2.5-1.86-3.301-3.306-.775-1.393-1.225-2.888-1.299-4.482-.02-.385.094-.522.477-.592a4.7 4.7 0 0 1 1.53-.038c2.131.311 3.946 1.264 5.467 2.774.868.86 1.525 1.887 2.202 2.89.72 1.066 1.494 2.082 2.48 2.915.348.291.626.513.892.677-.802.09-2.14.109-3.055-.615zm1.001-6.44a.306.306 0 0 1 .415-.287.3.3 0 0 1 .113.074.3.3 0 0 1 .086.214c0 .17-.136.307-.308.307a.303.303 0 0 1-.306-.307m3.11 1.596c-.2.081-.4.151-.591.16a1.25 1.25 0 0 1-.798-.254c-.274-.23-.47-.358-.551-.758a1.7 1.7 0 0 1 .015-.588c.07-.327-.007-.537-.238-.727-.188-.156-.426-.199-.689-.199a.6.6 0 0 1-.254-.078.253.253 0 0 1-.114-.358 1 1 0 0 1 .192-.21c.356-.202.767-.136 1.146.016.352.144.618.408 1.001.782.392.451.462.576.685.915.176.264.336.536.446.848.066.194-.02.353-.25.45"/>'},
  openai:{vb:'1.68 1.75 16.65 16.5',fill:'currentColor',svg:'<path d="M11.248 18.25q-.825 0-1.568-.314a4.3 4.3 0 0 1-1.32-.874 4 4 0 0 1-1.304.214 4 4 0 0 1-2.046-.544 4.27 4.27 0 0 1-1.518-1.485 4 4 0 0 1-.56-2.095q0-.48.131-1.04A4.4 4.4 0 0 1 2.04 10.71a4.07 4.07 0 0 1 .017-3.4 4.2 4.2 0 0 1 1.056-1.418 3.8 3.8 0 0 1 1.6-.842 3.9 3.9 0 0 1 .76-1.683q.593-.759 1.451-1.188a4.04 4.04 0 0 1 1.832-.429q.825 0 1.567.313.742.314 1.32.875a4 4 0 0 1 1.304-.215q1.106 0 2.046.545a4.14 4.14 0 0 1 1.501 1.485q.578.941.578 2.095 0 .48-.132 1.04.66.61 1.023 1.419.363.792.363 1.666 0 .892-.38 1.717a4.3 4.3 0 0 1-1.072 1.435 3.8 3.8 0 0 1-1.584.825 3.8 3.8 0 0 1-.775 1.683 4.06 4.06 0 0 1-1.436 1.188 4.04 4.04 0 0 1-1.832.429m-4.076-2.062q.825 0 1.435-.347l3.103-1.782a.36.36 0 0 0 .164-.313v-1.42L7.881 14.62a.67.67 0 0 1-.726 0l-3.118-1.798a.5.5 0 0 1-.017.115v.198q0 .841.396 1.551.413.693 1.139 1.089a3.2 3.2 0 0 0 1.617.412m.165-2.69a.4.4 0 0 0 .181.05q.083 0 .165-.05l1.238-.71-3.977-2.31a.7.7 0 0 1-.363-.643v-3.58q-.825.362-1.32 1.122a2.9 2.9 0 0 0-.495 1.65q0 .809.413 1.55.412.743 1.072 1.123zm3.91 3.663q.875 0 1.585-.396a2.96 2.96 0 0 0 1.534-2.64v-3.564a.32.32 0 0 0-.165-.297l-1.254-.726v4.604a.7.7 0 0 1-.363.643l-3.119 1.799a3 3 0 0 0 1.783.577m.627-6.039V8.878L10.01 7.822 8.129 8.878v2.244l1.881 1.056zM7.057 5.859a.7.7 0 0 1 .363-.644l3.119-1.798a3 3 0 0 0-1.782-.578q-.874 0-1.584.396A2.96 2.96 0 0 0 6.05 4.324a3.07 3.07 0 0 0-.396 1.551v3.547q0 .199.165.314l1.237.726zm8.383 7.887q.825-.364 1.303-1.123.495-.758.495-1.65a3.15 3.15 0 0 0-.412-1.55q-.413-.743-1.073-1.123l-3.086-1.782q-.099-.065-.181-.049a.3.3 0 0 0-.165.05l-1.238.692 3.993 2.327a.6.6 0 0 1 .264.264.64.64 0 0 1 .1.363zm-3.317-8.382a.63.63 0 0 1 .726 0l3.135 1.831v-.297q0-.792-.396-1.501a2.86 2.86 0 0 0-1.105-1.155q-.71-.43-1.65-.43-.825 0-1.436.347L8.294 5.941a.36.36 0 0 0-.165.314v1.418z"/>'},
  kimi:{vb:'0 0 24 24',fill:'currentColor',svg:'<title>Kimi</title><path d="M21.765.351C22.998.351 24 1.353 24 2.586S22.998 4.82 21.765 4.82h-1.974c-.15 0-.26-.12-.26-.26V2.586A2.237 2.237 0 0 1 21.765.35M9.41 13.388l8.447-8.377c.16-.16.07-.471-.14-.471h-4.55s-.1.02-.14.06l-9.099 9.029c-.14.14-.35.02-.35-.21V4.81c0-.15-.1-.27-.221-.27H.22c-.12 0-.22.12-.22.27v18.57c0 .15.1.27.22.27h3.137c.12 0 .22-.12.22-.27v-3.79c0-.08.03-.16.08-.21l2.826-2.796c.07-.07.16-.08.241-.03l7.546 5.551a8.9 8.9 0 0 0 4.018 1.493c.12.01.23-.11.23-.27V19.76c0-.14-.08-.25-.19-.26a5.8 5.8 0 0 1-2.355-.942l-6.533-4.73c-.14-.09-.15-.32-.03-.441"/>'},
  grok:{vb:'201.61 56.91 438.67 481.45',fill:'currentColor',svg:'<g> <polygon points="557.09,211.99 565.4,538.36 631.96,538.36 640.28,93.18 "/> <polygon points="640.28,56.91 538.72,56.91 379.35,284.53 430.13,357.05 "/> <polygon points="201.61,538.36 303.17,538.36 353.96,465.84 303.17,393.31 "/> <polygon points="201.61,211.99 430.13,538.36 531.69,538.36 303.17,211.99 "/> </g>'},
  gemini:{vb:'0 0 24 24',fill:'#8E75B2',svg:'<title>Google Gemini</title><path d="M11.04 19.32Q12 21.51 12 24q0-2.49.93-4.68.96-2.19 2.58-3.81t3.81-2.55Q21.51 12 24 12q-2.49 0-4.68-.93a12.3 12.3 0 0 1-3.81-2.58 12.3 12.3 0 0 1-2.58-3.81Q12 2.49 12 0q0 2.49-.96 4.68-.93 2.19-2.55 3.81a12.3 12.3 0 0 1-3.81 2.58Q2.49 12 0 12q2.49 0 4.68.96 2.19.93 3.81 2.55t2.55 3.81"/>'},
  custom:{vb:'0 0 24 24',fill:'currentColor',svg:'<path d="M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25zM20.71 7.04a1 1 0 0 0 0-1.41l-2.34-2.34a1 1 0 0 0-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83z"/>'}
};
/*BRAND-ICONS:END*/
let aiMessages = [];   // OpenAI 格式消息历史（内存 + localStorage 持久化，刷新页面不丢）
let aiBusy = false;
const $ai = id => document.getElementById(id);

/* ---------- 放大态 / 检查器 / 轨迹 / 文件引用的**状态** ----------
   声明必须早于 aiAdd 与 aiHistRestore：aiAdd 会读 aiTurnAnchor，
   而 let 变量在声明前处于 TDZ，读到就抛 ReferenceError（恢复历史时会炸）。
   所以状态放这里，函数留在后面的改造区块。 */
let aiBigOn = false;
let aiSideTab = 'trace';
let aiTurnNodes = [];      // 本回合轨迹节点（确认续接轮继续累加，不重置）
let aiStepAt = 0;          // 上一个"执行"节点的时间戳：用来算每一步的墙钟耗时
let aiTurnUsage = null;    // 本回合 token
let aiTurnStartAt = 0;
let aiTurnAnchor = null;   // 本回合第一条助手消息（轨迹挂回它，避免缺前半段）
let aiTurnLog = [];        // 会话内每回合用量摘要
let aiTraceView = null;    // 当前查看的历史回合元素（null = 本回合）
let aiAuditRows = null;
let aiAuditWin = null;     // 审计时间窗：从「轨迹」跳过来时只看本轮
let aiFiles = [];          // 待引用的文件路径
let aiPickPath = '';
let aiCurMission = null;   // 当前绑定的行动 ID（Phase 1）：随 /api/ai/chat 带 mission_id；
                           // 由 aiSendText 第二参设置，普通对话会清掉它
let aiMissionRows = null;  // 行动列表缓存（SSE 推送直接更新它）
let aiMissionES = null;    // 行动 SSE 句柄（只订阅一次，EventSource 自动重连）
const AI_KIND = {plan:'计划', step:'执行', result:'结果', ver:'复验', human:'等确认', grant:'授权', body:'传输', done:'结论'};
/* 允许"本任务内不再逐次询问"的工具（与服务端 aiGrantFamily 一一对应）。
   放在前端是为了**只在能批量的卡上显示那个勾选**——数据类/系统类动作不给这个选项，
   免得上去了又被服务端拒（那才是真的会误导人）。 */
const AI_BATCH_TOOLS = {start_service:1, stop_service:1, restart_service:1};

function aiOpen(){ $ai('ai-drawer').classList.add('open'); $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight; petShift(true);
  // 放大态：打开时把当前 tab 重画一遍（轨迹是流式追加的，重开抽屉要能接上）
  if(aiBigOn){ if(aiSideTab === 'trace') aiTraceRender(); if(aiSideTab === 'usage') aiUsageRender(); }
  // 焦点跟进输入框：抽屉是类 dialog 的浮层，打开后键盘焦点若留在下面的页面，
  // Tab 会"穿"到看不见的元素上（键盘用户直接迷路）。
  try{ $ai('ai-in').focus(); }catch(_){}
}
$ai('ai-close').onclick = ()=>{ $ai('ai-drawer').classList.remove('open'); aiPickClose(); petShift(false); };
$ai('ai-clear').onclick = ()=>{
  // 正在流式输出时点清空：必须先掐断流。不清的话，后续 SSE 事件会继续往
  // 已脱离文档的孤儿节点上贴卡片（表现为"清空后凭空冒出确认卡"），
  // aiBusy 也仍然为 true 直到流自然结束。
  if(aiCtrl){ try{ aiCtrl.abort(); }catch(_){} aiCtrl = null; }
  if(aiPending){ try{ aiPending.card.remove(); }catch(_){} aiPending = null; }
  aiMessages = []; try{ localStorage.removeItem('ek-ai-hist'); }catch(_){} $ai('ai-msgs').innerHTML = '<div class="ai-msg sys">' + t('对话已清空。') + '</div>';
  aiCurMission = null;   // 清空对话 = 解除行动绑定（行动本身仍在列表里，可从「行动」tab 继续）
  aiFiles = []; aiChipsRender(); aiPickClose(); aiTurnReset(); aiHstat(''); };

/* ---------- 彻底隐藏桌面助手（AI 设置页勾选项，localStorage 记忆） ---------- */
function applyPetGone(){
  let gone = false;
  try{ gone = localStorage.getItem('ek-pet-gone') === '1'; }catch(_){}
  const cb = document.getElementById('ai_pet_hide');
  if(cb) cb.checked = gone;
  document.body.classList.toggle('pet-gone', gone);
}
applyPetGone();
$ai('ai_pet_hide').addEventListener('change', function(){
  const gone = this.checked;
  try{ localStorage.setItem('ek-pet-gone', gone ? '1' : '0'); }catch(_){}
  document.body.classList.toggle('pet-gone', gone);
  toast(gone ? t('助手已彻底隐藏，可随时在这里恢复') : t('助手已回来啦'));
});

/* ---------- AI 用量统计：单次消耗行（聊天框）+ 今日累计与余额（桌宠悬停卡） ---------- */
function aiFmtTok(n){
  n = Math.round(Number(n)||0);
  // 百万级用 M：长对话的上下文动辄 5,000k，写成 "5463.1k" 没人读得出来量级
  if(n >= 1000000) return (n/1000000).toFixed(1) + 'M';
  return n >= 10000 ? (n/1000).toFixed(1) + 'k' : String(n);
}
function aiUsageAcc(){
  let u = {date:'', calls:0, tin:0, tout:0, hit:0, miss:0};
  try{ u = Object.assign(u, JSON.parse(localStorage.getItem('ek-ai-usage')||'{}')); }catch(_){}
  const today = new Date().toDateString();
  if(u.date !== today){ u = {date:today, calls:0, tin:0, tout:0, hit:0, miss:0}; }   // 按天清零
  return u;
}
function aiUsageAdd(u){
  if(!u) return;
  const a = aiUsageAcc();
  a.calls++;
  a.tin += Math.round(Number(u.prompt_tokens)||0);
  a.tout += Math.round(Number(u.completion_tokens)||0);
  a.hit += Math.round(Number(u.cache_hit)||0);
  a.miss += Math.round(Number(u.cache_miss)||0);
  try{ localStorage.setItem('ek-ai-usage', JSON.stringify(a)); }catch(_){}
}
let aiBal = {state:'idle', total:'', currency:'', available:null};   // idle|ok|no|err
async function aiBalanceRefresh(){
  try{
    const j = await (await fetch('/api/ai/balance')).json();
    if(!j.supported){ aiBal = {state:'no', total:'', currency:'', available:null}; }
    else if(j.ok){ aiBal = {state:'ok', total:String(j.total||''), currency:String(j.currency||''), available:j.available}; }
    else { aiBal = {state:'err', total:'', currency:'', available:null}; }
  }catch(e){ aiBal = {state:'err', total:'', currency:'', available:null}; }
}
/* j.available 是 DeepSeek 的 is_available：false = 账户被停用/欠费冻结，
   此时余额数字再好看也调不动模型。显示上必须把它单独说出来，
   否则用户看到"余额 ¥12.5"以为一切正常，实际每次请求都被上游拒。 */
function aiBalText(){
  const base = aiBal.currency === 'CNY' ? t('余额 ¥{0}', aiBal.total) : t('余额 {0} {1}', aiBal.total, aiBal.currency);
  return (aiBal.available === false) ? base + ' · ' + t('账户已停用') : base;
}
function petUsageRender(){
  const box = document.getElementById('pet-tipUsage'); if(!box) return;
  const u = aiUsageAcc();
  box.textContent = '';
  const sep = document.createElement('div'); sep.className = 'u-sep'; box.appendChild(sep);
  const hd = document.createElement('b'); hd.textContent = t('今日统计'); box.appendChild(hd);
  box.appendChild(document.createElement('br'));
  const l1 = document.createElement('span');
  l1.textContent = t('对话 {0} 次 · ↑{1} ↓{2}', u.calls, aiFmtTok(u.tin), aiFmtTok(u.tout));
  box.appendChild(l1); box.appendChild(document.createElement('br'));
  if(u.hit + u.miss > 0){
    const l2 = document.createElement('span');
    l2.textContent = t('缓存命中率 {0}%', Math.round(100*u.hit/(u.hit+u.miss)));
    box.appendChild(l2); box.appendChild(document.createElement('br'));
  }
  const l3 = document.createElement('span');
  if(aiBal.state === 'ok'){
    l3.textContent = aiBalText();
    if(aiBal.available === false) l3.style.color = 'var(--muted)';
  } else if(aiBal.state === 'no'){
    l3.textContent = t('仅 DeepSeek 支持');
    l3.style.color = 'var(--muted)';
  } else if(aiBal.state === 'err'){
    l3.textContent = t('余额查询失败');
  } else {
    l3.textContent = '…';
  }
  box.appendChild(l3);
  const ref = document.createElement('span');
  ref.className = 'bal-ref'; ref.textContent = '↻'; ref.title = t('刷新余额');
  ref.onclick = async ()=>{ ref.textContent = '…'; await aiBalanceRefresh(); petUsageRender(); };
  box.appendChild(ref);
}
aiBalanceRefresh();                                    // 进页面查一次余额（非 DeepSeek 静默降级）
setInterval(aiBalanceRefresh, 10*60*1000);             // 每 10 分钟静默刷新

/* 消息时间戳：MM-DD HH:mm，挂在每轮回合末尾的小工具栏里。
   原来那行气泡内左侧小灰字只有 HH:MM，用户反馈"看不清"——没有日期，
   跨天和多日前的对话根本定位不到是哪个回合。时间配月日后放进工具栏，
   "什么时候问的"和"复制/重新提问"放在一起，一眼找得到。
   ts 只认合法格式（本机生成/历史存档回读都过这关）；旧档是 HH:MM 无日期，
   也放行并按原样显示（不编日期）。 */
function aiTsOk(s){ return typeof s === 'string' && /^(([01]\d|2[0-3]):[0-5]\d|(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01]) ([01]\d|2[0-3]):[0-5]\d)$/.test(s); }
function aiNow(){ const d = new Date(); return ('0'+(d.getMonth()+1)).slice(-2)+'-'+('0'+d.getDate()).slice(-2)+' '+('0'+d.getHours()).slice(-2)+':'+('0'+d.getMinutes()).slice(-2); }

/* [envkit-verdict] 结论块是**给评测判据看的机器格式**（判据优先读它、词表降级兜底），
   不是给用户看的——用户原话"用户不需要看到"。
   **只在展示层剥**：SSE 原文、aiMessages 历史、localStorage 存档全部保持原样。
   在 Go 侧或入历史前剥，都会让判据的 verdict 覆盖率归零——沙箱采集的就是 SSE 原文。
   从第一次出现处切到文尾：块指令要求"块的三行之后不要再写别的内容"，
   所以切到文尾永不误伤正文；流式中途记号没打完（'[envkit-ver'）也一并切掉，
   免得半截记号在屏幕上闪一下。 */
function aiStripVerdict(s){
  // 守卫查公共前缀 '[envkit-ver'（完整记号与流式中途的半截都含它）——
  // 查完整记号的话，半截记号在早退这里就被放行了，下面两行剥离永远不执行
  if(typeof s !== 'string' || s.indexOf('[envkit-ver') < 0) return s;
  return s.replace(/\[envkit-verdict\][\s\S]*$/, '').replace(/\[envkit-ver$/, '').replace(/\s+$/, '');
}
function aiFill(d, text, opts){
  d.innerHTML = aiMd(aiStripVerdict(text));
  d.classList.add('ai-md');
  if(!opts || opts.hl !== false) aiHighlight(d);
}
function aiAdd(cls, text, ts, reask){
  const d = document.createElement('div');
  d.className = 'ai-msg ' + cls;
  /* data-no-i18n：模型/用户的原文不进翻译管线。
     两个收益：① 全局 i18n Observer 不再对每次流式 innerHTML 重建后的新子树
     整棵重扫（长回答下这是 O(n) 每帧，体感卡顿）；② 模型输出恰好和字典键
     撞车时不会被"顺手"翻成英文。静态界面文案照常翻译（它们不在这个容器里）。 */
  d.setAttribute('data-no-i18n', '');
  if(aiTsOk(ts)) d.dataset.ts = ts;
  // 本回合第一条助手消息：确认续接轮会再建一条，轨迹要挂回第一条才不会缺前半段
  if(cls === 'ai' && !aiTurnAnchor) aiTurnAnchor = d;
  aiFill(d, text);
  if(cls === 'ai'){
    // 复制给用户的也必须是剥干净的文本——结论块是调试信息，不进剪贴板
    d.dataset.raw = aiStripVerdict(text || '');
    const w = document.createElement('div'); w.className = 'ai-wrap';
    w.setAttribute('data-no-i18n', '');
    w.appendChild(d);
    w.appendChild(aiToolbar(d, reask));
    $ai('ai-msgs').appendChild(w);
  } else {
    $ai('ai-msgs').appendChild(d);
  }
  $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
  return d;
}
/* 回合末尾小工具栏：时间 + 复制 + 重新提问。
   reask 是触发这一回合的那条用户原话；为空（如「解读诊断报告」这种无用户提问的回合）
   就不放「重新提问」——没有可重问的问题，放一个按钮只会误导。 */
function aiToolbar(d, reask){
  const tb = document.createElement('div'); tb.className = 'ai-tb';
  if(d.dataset.ts){
    const tm = document.createElement('span'); tm.className = 'ai-tb-time'; tm.textContent = d.dataset.ts;
    tb.appendChild(tm);
  }
  const c = document.createElement('button'); c.className = 'ai-tb-btn'; c.textContent = t('复制');
  c.onclick = ()=>{
    const raw = d.dataset.raw || d.innerText;
    navigator.clipboard.writeText(raw).then(()=>{ c.textContent = t('已复制'); c.classList.add('done');
      setTimeout(()=>{ c.textContent = t('复制'); c.classList.remove('done'); }, 1600); })
      .catch(()=>{ c.textContent = t('失败'); setTimeout(()=>{ c.textContent = t('复制'); }, 1600); });
  };
  tb.appendChild(c);
  if(reask){
    const r = document.createElement('button'); r.className = 'ai-tb-btn'; r.textContent = t('重新提问');
    r.onclick = ()=>{ if(aiBusy){ toast(t('助手正在忙，稍后再试')); return; } aiSendText(reask); };
    tb.appendChild(r);
  }
  /* 轨迹 / 审计 / 导出：三个按钮都对应真实数据，不做装饰。
     「审计」按本轮轨迹的时间窗过滤——没有 trace_id 时这是最诚实的做法，
     比"猜哪几条属于这轮"可靠，也不会把别的回合算进来。 */
  const tr = document.createElement('button'); tr.className = 'ai-tb-btn'; tr.textContent = t('轨迹');
  tr.onclick = ()=>{ aiTraceView = d; aiSide('trace'); };
  tb.appendChild(tr);
  const au = document.createElement('button'); au.className = 'ai-tb-btn'; au.textContent = t('审计');
  au.onclick = ()=>{ aiAuditFocus(d.__trace); aiSide('audit'); };
  tb.appendChild(au);
  const ex = document.createElement('button'); ex.className = 'ai-tb-btn'; ex.textContent = t('导出');
  ex.onclick = ()=>aiExportTurn(d);
  tb.appendChild(ex);
  return tb;
}
/* 发送一段用户文本：发送按钮与「重新提问」共用。
   抽成函数是因为重新提问要复刻发送的全套动作——剥悬挂确认卡、记时间戳、进历史、
   起流式回合；两处各写一遍，迟早只改一处（确认卡悬挂那次事故就是这么来的）。 */
async function aiSendText(tx, missionId){
  if(aiBusy) return;
  tx = String(tx || '').trim();
  if(!tx) return;
  // 行动绑定（Phase 1）：带 missionId = 本回合是该行动的执行回合；
  // 不带 = 普通对话，清掉旧绑定（一次对话只属于一个行动，红线 5 同源）。
  aiCurMission = missionId || null;
  aiTurnReset();   // 新的一轮用户发言 = 新的一条轨迹（确认续接轮不重置）
  // 用户无视确认卡片直接发新消息：把悬挂的 assistant(tool_calls) 从历史里剥掉，
  // 否则下一轮请求历史变成 [..., assistant(tool_calls), user(新消息)]，上游直接 400
  if(aiPending){ aiPending.card.remove(); const ix = aiMessages.indexOf(aiPending.asst); if(ix >= 0) aiMessages.splice(ix, 1); aiPending = null; }
  const sendTs = aiNow();
  aiAdd('user', tx, sendTs);
  aiMessages.push({role:'user', content:tx, ts:sendTs});
  await aiConsume('/api/ai/chat', {messages: aiMessages});
}
/* ---------- Markdown 渲染 ----------
   以前这里是 4 行正则（围栏码 / 行内码 / 粗体 / 列点），模型输出的标题、有序列表、
   表格、链接全都退化成纯文本。现在交给 marked 解析，代码块交给 highlight.js。

   安全：模型输出是**不可信输入**（日志、网页、用户文件都可能进上下文），
   所以解析后过一遍白名单净化——脚本/事件属性/危险协议一律去掉，外链图片不加载。
   复制仍用 dataset.raw（原文），渲染方式的变化不影响剪贴板内容。 */
function aiEscHtml(s){
  return String(s == null ? '' : s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}
const AI_MD_OK = {P:1,BR:1,HR:1,STRONG:1,B:1,EM:1,I:1,DEL:1,S:1,CODE:1,PRE:1,
  BLOCKQUOTE:1,UL:1,OL:1,LI:1,H1:1,H2:1,H3:1,H4:1,H5:1,H6:1,A:1,
  TABLE:1,THEAD:1,TBODY:1,TR:1,TH:1,TD:1,INPUT:1};
function aiSanitizeHtml(html){
  const tpl = document.createElement('template');
  tpl.innerHTML = html;
  const bad = {SCRIPT:1,STYLE:1,IFRAME:1,OBJECT:1,EMBED:1,FORM:1,SVG:1,MATH:1,LINK:1,META:1};
  const walk = (root)=>{
    for(const el of Array.from(root.children)){
      const tag = el.tagName;
      if(bad[tag]){ el.remove(); continue; }
      if(!AI_MD_OK[tag]){
        // 认不出的标签脱壳保留文字（模型爱写 <div align=center> 这类）
        const frag = document.createDocumentFragment();
        while(el.firstChild) frag.appendChild(el.firstChild);
        el.replaceWith(frag);
        continue;
      }
      for(const at of Array.from(el.attributes)){
        if(at.name.toLowerCase().startsWith('on')) el.removeAttribute(at.name);
      }
      if(tag === 'A'){
        const href = el.getAttribute('href') || '';
        if(!/^(https?:|mailto:|#|\/)/i.test(href)) el.removeAttribute('href');
        else { el.setAttribute('target','_blank'); el.setAttribute('rel','noopener noreferrer'); }
      } else if(tag === 'INPUT'){
        if(el.getAttribute('type') !== 'checkbox'){ el.remove(); continue; }
        el.setAttribute('disabled','');
        for(const at of Array.from(el.attributes)){
          if(!['type','checked','disabled'].includes(at.name.toLowerCase())) el.removeAttribute(at.name);
        }
      } else if(tag === 'CODE'){
        const cls = (el.getAttribute('class') || '').split(/\s+/).filter(c=>/^language-[\w+-]+$/.test(c));
        if(cls.length) el.setAttribute('class', cls.join(' ')); else el.removeAttribute('class');
      } else {
        for(const at of Array.from(el.attributes)){
          const n = at.name.toLowerCase();
          if(n !== 'title' && !(n === 'align' && (tag === 'TH' || tag === 'TD' || tag === 'P'))) el.removeAttribute(at.name);
        }
      }
      walk(el);
    }
  };
  walk(tpl.content);
  return tpl.innerHTML;
}
function aiMd(t){
  const src = String(t == null ? '' : t);
  if(!window.marked || typeof marked.parse !== 'function') return aiEscHtml(src);
  let html;
  try{ html = marked.parse(src, {gfm:true, breaks:true, async:false}); }
  catch(e){ return aiEscHtml(src); }
  return aiSanitizeHtml(html);
}
function aiHighlight(root){
  if(!root || !window.hljs) return;
  try{
    root.querySelectorAll('pre code').forEach(el=>{
      if(el.dataset.hl === '1') return;
      try{ hljs.highlightElement(el); }catch(e){}
      el.dataset.hl = '1';
    });
  }catch(e){}
}
/* aiErr(msg)：参数名不能叫 t —— 那会遮蔽 i18n 的全局 t()，同作用域里
   一旦调用 t('…') 就变成对字符串取下标，静默返回 undefined。
   t(msg)：后端推下来的错误文案是中文原文，英文界面下按词表翻译；
   查不到的键 t() 原样返回，不会更糟。 */
function aiErr(msg){ aiAdd('err', t(msg)); }

/* ---------- 对话历史持久化：localStorage 存取，刷新/重开页面不丢 ---------- */
function aiHistSave(){
  try{
    const h = aiMessages.slice();
    // 尾部悬挂的 assistant(tool_calls)（确认卡还没批）不入档：恢复后缺 tool 结果会打坏下一轮请求
    while(h.length && h[h.length-1].tool_calls) h.pop();
    const slim = h.slice(-60).map(m => { const o = {role: m.role, content: String(m.content == null ? '' : m.content)}; if(aiTsOk(m.ts)) o.ts = m.ts; return o; });
    if(slim.length){ localStorage.setItem('ek-ai-hist', JSON.stringify(slim)); }
    else { localStorage.removeItem('ek-ai-hist'); }
  }catch(_){}
}
function aiHistRestore(){
  let h = null;
  try{ h = JSON.parse(localStorage.getItem('ek-ai-hist') || 'null'); }catch(_){}
  if(!Array.isArray(h)) return;
  aiMessages = h.filter(m => m && (m.role === 'user' || m.role === 'assistant') &&
    typeof m.content === 'string' && m.content.trim());
  if(!aiMessages.length) return;
  aiMessages.forEach(m => aiAdd(m.role === 'user' ? 'user' : 'ai', m.content, aiTsOk(m.ts) ? m.ts : undefined));
  aiAdd('sys', t('已恢复上次的对话（{0} 条）', aiMessages.length));
}
aiHistRestore();

/* ================= 放大态 / 检查器（轨迹·审计·用量） / 文件引用 =================
   四条设计约束（沿用项目既有口径，不是新发明）：
   1) 只加 DOM 与样式，不动主区任何面板；紧凑态仍是原来那条 430px 抽屉。
   2) 轨迹的颜色只表达三态（中性/通过/失败）；人工介入单独一种，因为它决定
      "这次算不算自主" —— 与 trace 的口径一致。
   3) 审计树的数据全部来自 /api/audit 的 trace_id 分组，前端不重新判定结果，
      只做"把已有事实排成树"这件事。
   4) 文件选择只产出"路径引用"，不读文件内容——读文件是 AI 的 read_file 在沙箱内
      的职责；在浏览器里另开一条读取路径，等于绕过他们自己设的边界。 */

/* 状态变量在上面（aiMessages 旁）声明：aiAdd 在页面加载恢复历史时就会读 aiTurnAnchor。 */

function aiHstat(s){ const el = $ai('ai-hstat'); if(el) el.textContent = s || ''; }
function aiClock(ms){
  const d = new Date(ms || Date.now());
  return ('0'+d.getHours()).slice(-2)+':'+('0'+d.getMinutes()).slice(-2)+':'+('0'+d.getSeconds()).slice(-2);
}

function aiSide(tab){
  const dr = $ai('ai-drawer'); if(!dr) return;
  if(!dr.classList.contains('open')) aiOpen();
  if(!dr.classList.contains('big')){
    aiBigOn = true; dr.classList.add('big');
    const b = $ai('ai-expand'); if(b){ b.textContent = '⤡'; b.title = t('还原'); }
  }
  aiSideTab = tab || 'trace';
  document.querySelectorAll('#ai-tabs button').forEach(x=>x.classList.toggle('on', x.dataset.tab === aiSideTab));
  ['trace','audit','usage','missions'].forEach(k=>{ const p = $ai('ai-pane-'+k); if(p) p.hidden = (k !== aiSideTab); });
  if(aiSideTab === 'trace') aiTraceRender();
  if(aiSideTab === 'audit') aiAuditLoad();
  if(aiSideTab === 'usage') aiUsageRender();
  if(aiSideTab === 'missions') aiMissionsLoad();
}
function aiBig(on){
  const dr = $ai('ai-drawer'); if(!dr) return;
  aiBigOn = !!on;
  dr.classList.toggle('big', aiBigOn);
  const b = $ai('ai-expand');
  if(b){ b.textContent = aiBigOn ? '⤡' : '⤢'; b.title = t(aiBigOn ? '还原' : '放大'); }
  if(aiBigOn) aiSide(aiSideTab); else aiPickClose();
}

/* ---------- 轨迹：本回合的 step 时间轴 ---------- */
function aiTurnReset(){
  aiTurnNodes = []; aiTurnUsage = null; aiTurnStartAt = Date.now(); aiTurnAnchor = null; aiTraceView = null; aiStepAt = 0;
  aiTraceRender(); aiUsageRender();
}
function aiTraceAdd(kind, text, opt){
  const n = {kind: kind, text: String(text || ''), cls: '', detail: '', at: Date.now()};
  if(opt) Object.assign(n, opt);
  // 每步耗时：从"执行"到"结果"的墙钟差。不向服务端多要字段——
  // 事件到达顺序本身就是时序证据，而且这个数才是用户感知的"这一步花了多久"。
  if(kind === 'step') aiStepAt = n.at;
  else if((kind === 'result' || kind === 'ver') && aiStepAt && !n.dur) n.dur = n.at - aiStepAt;
  aiTurnNodes.push(n);
  if(aiBigOn && aiSideTab === 'trace') aiTraceRender();
  return n;
}
/* 一个事件 → 一个节点。**只在展示层做归类**：判据（ok/verified）始终来自服务端字段，
   前端不重新判断成败——那正是"徽章说验了就是真验了"的前提。 */
function aiTraceEvent(ev){
  if(!ev || !ev.type) return;
  if(ev.type === 'plan_start'){
    aiTraceAdd('plan', t('预算 {0} 轮', ev.max_turns) + (ev.reason ? ' · ' + ev.reason : ''));
  } else if(ev.type === 'plan_extend'){
    aiTraceAdd('plan', t('上限放宽到 {0} 轮', ev.max_turns), {cls:'warn', detail: ev.reason || ''});
  } else if(ev.type === 'plan_step'){
    aiTraceAdd('step', (ev.seq ? ev.seq + '. ' : '') + (ev.text || aiToolName(ev.tool)),
      {detail: ev.target ? ('target: ' + ev.target) : ''});
  } else if(ev.type === 'tool_result'){
    const rs = String(ev.result || '');
    /* 成败判据优先用服务端的 ok 字段（v3.3 起 tool_result 带上）。
       旧的中文正则只作旧版本后端的兜底 —— 它连 "connection refused"
       这种英文报错都标不红，而且和"前端不重新判定结果"的原则相悖。 */
    const bad = (typeof ev.ok === 'boolean') ? !ev.ok : /^(错误|执行出错)|失败|拒绝|不在白名单/.test(rs);
    aiTraceAdd('result', aiToolName(ev.tool), {cls: bad ? 'bad' : '', detail: rs});
  } else if(ev.type === 'verify_result'){
    aiTraceAdd('ver', ev.ok ? (ev.verified ? t('已复验通过') : t('已执行·未确认')) : t('复验未通过'), {
      cls: ev.ok ? (ev.verified ? 'ok' : 'warn') : 'bad',
      detail: [ev.msg].concat(Array.isArray(ev.evidence) ? ev.evidence : []).filter(Boolean).join('\n')
    });
  } else if(ev.type === 'confirm_request'){
    aiTraceAdd('human', t('等待你确认：{0}', aiToolName(ev.tool)), {cls:'human', detail: ev.text || ''});
    aiHstat(t('等你确认'));
  } else if(ev.type === 'plan_end'){
    aiTraceAdd('done', ev.text || t('已得出结论'), {cls:'ok'});
  } else if(ev.type === 'usage'){
    aiTurnUsage = ev.usage || null;
    if(aiTurnUsage) aiHstat(t('↑{0} ↓{1}', aiFmtTok(aiTurnUsage.prompt_tokens), aiFmtTok(aiTurnUsage.completion_tokens)));
    if(aiBigOn && aiSideTab === 'usage') aiUsageRender();
  } else if(ev.type === 'error'){
    aiTraceAdd('done', String(ev.text || ''), {cls:'bad'});
  }
}
function aiTraceRender(){
  const box = $ai('ai-pane-trace'); if(!box) return;
  const nodes = (aiTraceView && Array.isArray(aiTraceView.__trace)) ? aiTraceView.__trace : aiTurnNodes;
  box.textContent = '';
  // 头部先给结论：几步、多久、花多少、命中多少。明细在下面，别让人先读一堆时间戳。
  const head = document.createElement('div'); head.className = 'muted';
  head.style.cssText = 'margin-bottom:8px;display:flex;flex-wrap:wrap;gap:6px;align-items:center;line-height:1.5';
  if(aiTraceView){
    head.appendChild(document.createTextNode(t('历史回合的轨迹（只读）')));
    const back = document.createElement('button'); back.textContent = t('回到本回合');
    back.style.cssText = 'background:none;border:1px solid var(--line);border-radius:6px;color:var(--muted);font-size:11px;padding:1px 7px;cursor:pointer';
    back.onclick = ()=>{ aiTraceView = null; aiTraceRender(); };
    head.appendChild(back);
  } else {
    const bits = [t('{0} 个节点', (nodes || []).length)];
    if(aiTurnStartAt) bits.push(((Date.now() - aiTurnStartAt) / 1000).toFixed(1) + 's');
    if(aiTurnUsage){
      bits.push('↑' + aiFmtTok(aiTurnUsage.prompt_tokens) + ' ↓' + aiFmtTok(aiTurnUsage.completion_tokens));
      const p = aiCachePct(aiTurnUsage);
      if(p !== null) bits.push(t('缓存 {0}%', Math.round(p * 100)));
    }
    head.appendChild(document.createTextNode(t('本回合') + ' · ' + bits.join(' · ')));
  }
  box.appendChild(head);
  if(!nodes || !nodes.length){
    const p = document.createElement('div'); p.className = 'muted'; p.textContent = t('还没有轨迹。发一句话就会有了。');
    box.appendChild(p); return;
  }
  const ul = document.createElement('ul'); ul.className = 'tl';
  nodes.forEach(n=>{
    const li = document.createElement('li'); li.className = 'tl-row ' + (n.cls || '');
    const dot = document.createElement('i'); dot.className = 'tl-dot';
    const k = document.createElement('span'); k.className = 'tl-k'; k.textContent = aiClock(n.at);
    const kd = document.createElement('span'); kd.className = 'tl-k'; kd.textContent = t(AI_KIND[n.kind] || n.kind);
    const tt = document.createElement('span'); tt.className = 'tl-t'; tt.textContent = n.text;
    li.appendChild(dot); li.appendChild(k); li.appendChild(kd); li.appendChild(tt);
    if(n.dur){ const d = document.createElement('span'); d.className = 'tl-dur'; d.textContent = (n.dur / 1000).toFixed(1) + 's'; li.appendChild(d); }
    if(n.detail){
      const lines = n.detail.split('\n').length;
      if(lines > 8){
        // 长证据默认折叠：轨迹是看时序的，不是看日志的；要看再展开
        const det = document.createElement('details'); det.className = 'tl-det';
        const sm = document.createElement('summary'); sm.textContent = t('展开明细（{0} 行）', lines);
        const pre = document.createElement('div'); pre.className = 'tl-xbody'; pre.textContent = n.detail;
        det.appendChild(sm); det.appendChild(pre); li.appendChild(det);
      } else {
        const x = document.createElement('div'); x.className = 'tl-x'; x.textContent = n.detail; li.appendChild(x);
      }
    }
    ul.appendChild(li);
  });
  box.appendChild(ul);
  // 图例：三种状态色 + 人工点。颜色含义只有一份口径，写在页面上而不是只在注释里。
  const lg = document.createElement('div'); lg.className = 'tl-legend';
  [['ok', t('已验证')], ['warn', t('已执行·未确认')], ['bad', t('失败')], ['human', t('人工确认')]].forEach(x=>{
    const s = document.createElement('span'); s.className = x[0];
    const i = document.createElement('i');
    const e = document.createElement('em'); e.textContent = x[1];
    s.appendChild(i); s.appendChild(e); lg.appendChild(s);
  });
  box.appendChild(lg);
}

/* ---------- 审计分支树：trace_id 做根，actor 做泳道 ---------- */
function aiAuditFocus(nodes){
  aiAuditWin = null;
  if(nodes && nodes.length){
    const from = nodes[0].at - 5000, to = nodes[nodes.length-1].at + 5000;
    aiAuditWin = {from: from, to: to};
  }
}
function aiAuditLoad(force){
  const box = $ai('ai-pane-audit'); if(!box) return;
  if(!box.dataset.shell){
    box.dataset.shell = '1';
    box.innerHTML =
      '<div class="pbar">' +
        '<input id="ai-audit-q" placeholder="' + esc(t('筛选动作 / 目标')) + '">' +
        '<label class="muted" style="display:flex;gap:4px;align-items:center;white-space:nowrap"><input type="checkbox" id="ai-audit-onlybad">' + esc(t('只看异常')) + '</label>' +
        '<button id="ai-audit-re">' + esc(t('刷新')) + '</button>' +
      '</div>' +
      '<div id="ai-audit-win"></div>' +
      '<div id="ai-audit-list"></div>';
    $ai('ai-audit-re').onclick = ()=>aiAuditLoad(true);
    $ai('ai-audit-onlybad').onchange = ()=>aiAuditRender();
    $ai('ai-audit-q').oninput = ()=>aiAuditRender();
  }
  const list = $ai('ai-audit-list');
  if(list && !aiAuditRows) list.textContent = t('读取审计…');
  if(aiAuditRows && !force){ aiAuditRender(); return; }
  fetch('/api/audit?n=400&day=all').then(r=>r.json()).then(d=>{
    aiAuditRows = (d && d.entries) || [];
    aiAuditRender();
  }).catch(()=>{ if(list) list.textContent = t('读取失败'); });
}
function aiAuditRender(){
  const list = $ai('ai-audit-list'); if(!list) return;
  const q = (($ai('ai-audit-q') && $ai('ai-audit-q').value) || '').trim().toLowerCase();
  const onlyBad = !!($ai('ai-audit-onlybad') && $ai('ai-audit-onlybad').checked);
  const winBox = $ai('ai-audit-win');
  if(winBox){
    winBox.textContent = '';
    if(aiAuditWin){
      const tip = document.createElement('div'); tip.className = 'muted';
      tip.style.cssText = 'margin-bottom:6px;display:flex;gap:6px;align-items:center';
      tip.appendChild(document.createTextNode(t('已按本轮时间窗过滤')));
      const clr = document.createElement('button');
      clr.textContent = t('清除'); clr.style.cssText = 'background:none;border:1px solid var(--line);border-radius:6px;color:var(--muted);font-size:11px;padding:1px 7px;cursor:pointer';
      clr.onclick = ()=>{ aiAuditWin = null; aiAuditRender(); };
      tip.appendChild(clr); winBox.appendChild(tip);
    }
  }
  list.textContent = '';
  const rowsAll = aiAuditRows || [];
  if(!rowsAll.length){ const p = document.createElement('div'); p.className='muted'; p.textContent = t('暂无审计记录'); list.appendChild(p); return; }
  const inWin = e=>{
    if(!aiAuditWin) return true;
    const ts = Date.parse(String(e.ts || '').replace(' ', 'T'));
    return !isFinite(ts) || (ts >= aiAuditWin.from && ts <= aiAuditWin.to);
  };
  const isBad = e=>e.result === 'fail' || e.result === 'denied' || e.result === 'unverified';
  const match = e=>{
    if(onlyBad && !isBad(e)) return false;
    if(!q) return true;
    return (String(e.action||'') + ' ' + String(e.target||'') + ' ' + String(e.detail||'')).toLowerCase().indexOf(q) >= 0;
  };
  // 分组：一条 trace = 一次任务；无 trace_id 的归到"未归属"
  const groups = [], ix = new Map();
  for(const e of rowsAll){
    if(!inWin(e)) continue;
    const k = e.trace_id || '_';
    let g = ix.get(k);
    if(!g){ g = {k: k, rows: []}; ix.set(k, g); groups.push(g); }
    g.rows.push(e);
  }
  // 有 trace_id 的组 = 一次真实任务，排前面；「未归属」（按钮触发的零散操作）
  // 永远是最大的一坨（实测一天 294 条），放最后并且默认折叠，否则它会把树撑爆。
  groups.sort((a, b)=>(a.k === '_' ? 1 : 0) - (b.k === '_' ? 1 : 0));
  let shown = 0;
  for(const g of groups){
    const rows = g.rows.filter(match);
    if(!rows.length) continue;
    shown++;
    const worst = rows.some(e=>e.result === 'fail' || e.result === 'denied') ? 'bad'
      : (rows.some(e=>e.result === 'unverified' || e.result === 'started') ? 'warn' : 'ok');
    const det = document.createElement('details');
    det.className = 'tr-root ' + worst;
    // 默认只展开"真的失败/被拒"的那几条：正常 trace 与「未归属」都收成一行。
    // 全展开的话 40 条 trace × 3 行 ≈ 120 行，等于把表格又搬回来了；
    // 而"未复验"不该自动抢注意力——它是常态，不是故障。
    det.open = (g.k !== '_') && rows.some(e=>e.result === 'fail' || e.result === 'denied');
    const sm = document.createElement('summary'); sm.className = 'tr-head';
    const id = document.createElement('span'); id.className = 'tr-id'; id.textContent = g.k === '_' ? t('未归属') : g.k;
    const sum = document.createElement('span'); sum.className = 'tr-sum';
    const cnt = {user:0, ai:0, guard:0, system:0, shadow:0};
    rows.forEach(e=>{ if(cnt[e.actor] != null) cnt[e.actor]++; });
    sum.textContent = t('{0} 条', rows.length) + '  ' +
      Object.keys(cnt).filter(k=>cnt[k]).map(k=>k + ' ' + cnt[k]).join(' · ');
    const tg = document.createElement('span'); tg.className = 'tr-tag ' + worst;
    tg.textContent = worst === 'bad' ? t('有异常') : (worst === 'warn' ? t('未复验') : t('正常'));
    sm.appendChild(id); sm.appendChild(sum); sm.appendChild(tg);
    det.appendChild(sm);
    // 同 (actor, action, target, result) 合成一行：一天 294 条 env_check 压成「×286」一行，
    // 想看哪一次再点开。树是给人看"这次任务发生了什么"的，不是给日志做镜像。
    const agg = [], ax = new Map();
    for(const e of rows){
      const key = [e.actor, e.action, e.target, e.result].join('|');
      let a = ax.get(key);
      if(!a){ a = {e: e, n: 0, dur: 0, rows: []}; ax.set(key, a); agg.push(a); }
      a.n++; a.dur += Number(e.dur_ms) || 0; a.rows.push(e);
    }
    for(const a of agg){
      const e = a.e;
      const r = document.createElement('div'); r.className = 'tr-row';
      const ac = document.createElement('span'); ac.className = 'tr-act'; ac.textContent = e.actor || '';
      const n = document.createElement('span'); n.className = 'tr-name';
      n.textContent = (e.action || '') + (e.target ? ' · ' + e.target : '') + (a.n > 1 ? '  ×' + a.n : '');
      const tag = document.createElement('span'); tag.className = 'tr-tag ' + (isBad(e) ? 'bad' : (e.result === 'started' ? 'warn' : 'ok'));
      tag.textContent = e.result || '';
      r.appendChild(ac); r.appendChild(n); r.appendChild(tag);
      if(a.dur){ const d = document.createElement('span'); d.className = 'tr-id'; d.textContent = a.dur + 'ms'; r.appendChild(d); }
      r.onclick = ()=>{
        const exist = r.nextElementSibling;
        if(exist && exist.classList.contains('tr-detail')){ exist.remove(); return; }
        const box = document.createElement('div'); box.className = 'tr-detail';
        box.textContent = a.rows.map(x=>[('ts: ' + (x.ts || '')), ('verify: ' + (x.verify || '-')),
          ('params: ' + (x.params || '-')), ('detail: ' + (x.detail || '-'))].join('  ')).join('\n');
        r.after(box);
      };
      det.appendChild(r);
    }
    list.appendChild(det);
  }
  if(!shown){ const p = document.createElement('div'); p.className='muted'; p.textContent = t('没有命中'); list.appendChild(p); }
}

/* ---------- 用量：DSH 式明细卡 + 会话/今日汇总 ---------- */
function aiNum(n){ n = Math.round(Number(n) || 0); return n.toLocaleString('en-US'); }
function aiCachePct(u){
  const hit = Number(u && u.cache_hit) || 0, miss = Number(u && u.cache_miss) || 0;
  if(hit + miss <= 0) return null;
  return hit / (hit + miss);
}
function aiUsageBrief(u){
  if(!u) return '';
  const bits = ['↑' + aiFmtTok(u.prompt_tokens), '↓' + aiFmtTok(u.completion_tokens)];
  const p = aiCachePct(u);
  if(p !== null) bits.push(t('缓存 {0}%', Math.round(p * 100)));
  return bits.join(' · ');
}
/* 一张卡答四件事：花了多少、命中多少、哪个模型、合计多少。
   行序照 DSH：提供方/模型 → 命中率 → 未缓存输入 → 缓存读取 → 缓存写入 → 输出。 */
function aiUsageCard(u, title, note){
  u = u || {};
  const card = document.createElement('div'); card.className = 'ai-ucard';
  const hit = Number(u.cache_hit) || 0, missRaw = Number(u.cache_miss) || 0;
  const prompt = Number(u.prompt_tokens) || 0, out = Number(u.completion_tokens) || 0;
  // 有的上游只报 prompt + hit，不单独报 miss：缺口按 prompt-hit 兜底，而不是显示 0
  const uncached = missRaw > 0 ? missRaw : Math.max(0, prompt - hit);
  const pct = (hit + uncached) > 0 ? (100 * hit / (hit + uncached)) : null;
  const head = document.createElement('div'); head.className = 'uc-head';
  const hb = document.createElement('b'); hb.textContent = title;
  const hv = document.createElement('span'); hv.textContent = aiNum(prompt + out) + ' tok';
  head.appendChild(hb); head.appendChild(hv); card.appendChild(head);
  const row = (k, v, cls)=>{
    const r = document.createElement('div'); r.className = 'uc-row' + (cls ? ' ' + cls : '');
    const a = document.createElement('span'); a.textContent = k;
    const b = document.createElement('b'); b.textContent = v;
    r.appendChild(a); r.appendChild(b); card.appendChild(r);
  };
  row(t('提供方 / 模型'), ([u.provider, u.model].filter(Boolean).join('/')) || '—');
  row(t('缓存命中'), pct === null ? '—' : pct.toFixed(1) + '%', pct !== null && pct >= 50 ? 'hi' : '');
  row(t('未缓存输入'), aiNum(uncached) + ' tok');
  row(t('缓存读取'), aiNum(hit) + ' tok');
  // 「缓存写入」行已删除（v3.3）：后端 usage 里根本没有这个字段，
  // 原来那行 '0 tok' 是画出来的假数据 —— 展示一个永远为 0 的编造值，
  // 不如不展示（DeepSeek 的计费说明已在卡片底部的 note 里）。
  row(t('输出'), aiNum(out) + ' tok');
  if(note){ const n = document.createElement('div'); n.className = 'uc-note'; n.textContent = note; card.appendChild(n); }
  return card;
}
/* 消息尾部的用量行：默认只显示一行摘要，点开才是那张卡——
   聊天区保持干净，但"这一轮到底花了多少"随时可查。 */
function aiUsageLine(host, u){
  if(!host) return;
  host.__usage = u;
  const oldLine = host.querySelector('.ai-usage'); if(oldLine) oldLine.remove();
  const oldCard = host.querySelector('.ai-ucard'); if(oldCard) oldCard.remove();
  const ln = document.createElement('div'); ln.className = 'ai-usage';
  ln.textContent = aiUsageBrief(u) + '  ▾';
  ln.title = t('点开看本轮用量明细');
  const card = aiUsageCard(u, t('本轮用量'), t('缓存写入一栏对 DeepSeek 恒为 0：未命中的输入按普通输入计费，不单列写入。'));
  card.hidden = true;
  ln.onclick = ()=>{ card.hidden = !card.hidden; ln.textContent = aiUsageBrief(u) + (card.hidden ? '  ▾' : '  ▴'); };
  host.appendChild(ln);
  host.appendChild(card);
}
function aiUsageRender(){
  const box = $ai('ai-pane-usage'); if(!box) return;
  box.textContent = '';
  if(!aiTurnUsage && !aiTurnLog.length){
    const tip = document.createElement('div'); tip.className = 'muted'; tip.style.marginBottom = '8px';
    tip.textContent = t('还没有数据。发一条消息后这里会显示 token、步数与耗时。');
    box.appendChild(tip);
  }
  if(aiTurnUsage){
    box.appendChild(aiUsageCard(aiTurnUsage, t('本轮用量'),
      t('缓存写入一栏对 DeepSeek 恒为 0：未命中的输入按普通输入计费，不单列写入。')));
  }
  const grid = (pairs)=>{
    const g = document.createElement('div'); g.className = 'us-grid';
    pairs.forEach(p=>{ const b = document.createElement('b'); b.textContent = p[0]; const s = document.createElement('span'); s.textContent = p[1]; g.appendChild(b); g.appendChild(s); });
    return g;
  };
  const sum = aiTurnLog.reduce((a,x)=>({tin:a.tin+x.tin, tout:a.tout+x.tout, hit:a.hit+x.hit, miss:a.miss+x.miss, n:a.n+1}),
    {tin:0, tout:0, hit:0, miss:0, n:0});
  const h2 = document.createElement('div'); h2.className = 'muted'; h2.style.margin = '10px 0 4px';
  h2.textContent = t('本会话 · {0} 轮', sum.n); box.appendChild(h2);
  box.appendChild(grid([
    [t('输入'), aiFmtTok(sum.tin)], [t('输出'), aiFmtTok(sum.tout)],
    [t('平均每轮'), aiFmtTok(sum.n ? (sum.tin + sum.tout) / sum.n : 0)],
    [t('缓存命中'), (sum.hit + sum.miss) > 0 ? Math.round(100 * sum.hit / (sum.hit + sum.miss)) + '%' : '—']
  ]));
  if(aiTurnLog.length){
    const bars = document.createElement('div'); bars.className = 'us-bars';
    const max = Math.max.apply(null, aiTurnLog.map(x=>x.tin + x.tout)) || 1;
    aiTurnLog.slice(-16).forEach((x,i)=>{
      const b = document.createElement('div'); b.className = 'us-bar';
      b.style.height = Math.max(2, Math.round(44 * (x.tin + x.tout) / max)) + 'px';
      const p = (x.hit + x.miss) > 0 ? Math.round(100 * x.hit / (x.hit + x.miss)) + '%' : '—';
      b.title = '#' + (aiTurnLog.length - Math.min(16, aiTurnLog.length) + i + 1) +
        ' ↑' + x.tin + ' ↓' + x.tout + ' · 缓存 ' + p + ' · ' + x.steps + ' 步';
      bars.appendChild(b);
    });
    box.appendChild(bars);
    // 逐轮明细：定位"哪一轮掉点"靠它（合计永远看不出冷启动/重建）
    const hh = document.createElement('div'); hh.className = 'muted'; hh.style.margin = '10px 0 4px';
    hh.textContent = t('最近回合'); box.appendChild(hh);
    aiTurnLog.slice(-8).reverse().forEach(x=>{
      const r = document.createElement('div'); r.className = 'uc-row';
      const a = document.createElement('span');
      const p = (x.hit + x.miss) > 0 ? Math.round(100 * x.hit / (x.hit + x.miss)) + '%' : '—';
      a.textContent = (x.at || '') + ' · ↑' + aiFmtTok(x.tin) + ' ↓' + aiFmtTok(x.tout) +
        ' · 缓存 ' + p + ' · ' + x.steps + ' 步';
      const b = document.createElement('b'); b.textContent = (x.ms ? (x.ms / 1000).toFixed(1) + 's' : '');
      r.appendChild(a); r.appendChild(b); box.appendChild(r);
    });
  }
  const a = aiUsageAcc();
  const h3 = document.createElement('div'); h3.className = 'muted'; h3.style.margin = '10px 0 4px';
  h3.textContent = t('今日（本机累计）'); box.appendChild(h3);
  box.appendChild(grid([[t('对话'), String(a.calls)], [t('输入'), aiFmtTok(a.tin)], [t('输出'), aiFmtTok(a.tout)],
    [t('缓存命中'), (a.hit + a.miss) > 0 ? Math.round(100 * a.hit / (a.hit + a.miss)) + '%' : '—']]));
  const note = document.createElement('div'); note.className = 'muted'; note.style.marginTop = '8px'; note.style.lineHeight = '1.5';
  note.textContent = t('会话统计只在本次页面打开期间累积；今日统计写在本机 localStorage。');
  box.appendChild(note);
}

/* ---------- 行动（Mission）：Agent 闭环 Phase 1 ----------
   数据源 GET /api/missions；实时 /api/missions/events SSE（EventSource 自动重连，
   「刷新」兜底）。状态徽章**只显示服务端字段**——前端不重新判定成败（红线 1），
   与 tool_result/verify_result 徽章同一哲学。 */
const AI_MS_STATE = {
  pending:'待执行', awaiting_confirm:'待确认', running:'执行中', paused:'待你继续',
  done:'完成', failed:'失败', canceled:'已取消', expired:'已过期'
};
const AI_MS_CLS = {
  pending:'', awaiting_confirm:'warn', running:'run', paused:'warn',
  done:'ok', failed:'bad', canceled:'dim', expired:'dim'
};
const AI_MS_TRIGGER = {manual:'手动', patrol:'巡检', schedule:'晨报', webhook:'入站', guard:'守护'};

function aiMissionsShell(){
  const box = $ai('ai-pane-missions'); if(!box) return null;
  if(!box.dataset.shell){
    box.dataset.shell = '1';
    box.innerHTML =
      '<div class="pbar">' +
        '<input id="ai-ms-goal" placeholder="' + esc(t('描述要让助手做什么…')) + '">' +
        '<button id="ai-ms-new">' + esc(t('新建行动')) + '</button>' +
        '<button id="ai-ms-re">' + esc(t('刷新')) + '</button>' +
      '</div>' +
      '<div id="ai-ms-list"></div>';
    $ai('ai-ms-new').onclick = aiMissionsCreate;
    $ai('ai-ms-re').onclick = ()=>aiMissionsLoad(true);
    $ai('ai-ms-goal').addEventListener('keydown', e=>{ if(e.key === 'Enter') aiMissionsCreate(); });
  }
  return box;
}

function aiMissionsLoad(force){
  const box = aiMissionsShell(); if(!box) return;
  aiMissionsSubscribe();
  const list = $ai('ai-ms-list');
  if(list && aiMissionRows == null) list.textContent = t('读取行动…');
  if(aiMissionRows && !force){ aiMissionsRender(); return; }
  fetch('/api/missions').then(r=>r.json()).then(d=>{
    aiMissionRows = (d && d.missions) || [];
    aiMissionsRender();
  }).catch(()=>{ if(list) list.textContent = t('读取失败'); });
}

function aiMissionsSubscribe(){
  if(aiMissionES) return;
  try{
    aiMissionES = new EventSource('/api/missions/events?t=' + encodeURIComponent(window.__EK_TOKEN__));
    aiMissionES.onmessage = e=>{
      try{
        const j = JSON.parse(e.data);
        if(j && j.type === 'missions'){
          aiMissionRows = j.missions || [];
          if(aiSideTab === 'missions') aiMissionsRender();
        }
      }catch(_){}
    };
    // onerror 不处理：EventSource 自动重连；连不上时靠「刷新」按钮兜底
  }catch(_){ aiMissionES = null; }
}

function aiMissionsRender(){
  const list = $ai('ai-ms-list'); if(!list) return;
  list.textContent = '';
  const rows = aiMissionRows || [];
  if(!rows.length){
    const p = document.createElement('div'); p.className = 'muted';
    p.textContent = t('暂无行动。可在下方新建，或由巡检/入站触发自动产出。');
    list.appendChild(p);
    return;
  }
  rows.forEach(m=>{
    const row = document.createElement('div');
    row.className = 'ai-ms-row' + (AI_MS_CLS[m.state] ? ' ' + AI_MS_CLS[m.state] : '');
    const main = document.createElement('div'); main.className = 'ai-ms-main';

    const head = document.createElement('div'); head.className = 'ai-ms-head';
    const badge = document.createElement('span');
    badge.className = 'ai-ms-badge';
    badge.textContent = t(AI_MS_STATE[m.state] || m.state);
    head.appendChild(badge);
    const kind = document.createElement('span'); kind.className = 'ai-ms-kind muted';
    kind.textContent = t((AI_MS_TRIGGER[(m.trigger||{}).kind]) || (m.trigger||{}).kind || '');
    head.appendChild(kind);
    const at = document.createElement('span'); at.className = 'ai-ms-at muted';
    const ts = Date.parse(String(m.updated_at || '').replace(' ', 'T'));
    at.textContent = isFinite(ts) ? new Date(ts).toLocaleTimeString() : '';
    head.appendChild(at);
    main.appendChild(head);

    const goal = document.createElement('div'); goal.className = 'ai-ms-goal';
    goal.textContent = m.goal || '';
    main.appendChild(goal);

    // 说明行：暂停原因 / 终态结论 / 断点——全部是服务端原文，esc 由 textContent 保证
    let note = '';
    if(m.state === 'paused' || m.state === 'awaiting_confirm') note = m.pause_why || '';
    else if(m.result && m.result.msg) note = (m.result.verified ? '✓ ' : '✗ ') + m.result.msg;
    if(note){
      const n = document.createElement('div'); n.className = 'ai-ms-note muted';
      n.textContent = note;
      main.appendChild(n);
    }
    row.appendChild(main);

    const acts = document.createElement('div'); acts.className = 'ai-ms-acts';
    const canRun = ['pending','paused','awaiting_confirm'].indexOf(m.state) >= 0;
    const isRun = m.state === 'running';
    const open = !['done','failed','canceled','expired'].includes(m.state);
    if(canRun){
      const b = document.createElement('button');
      b.textContent = t('继续'); b.title = t('继续执行该行动');
      b.onclick = ()=>aiMissionsResume(m);
      acts.appendChild(b);
    }
    if(isRun){
      const b = document.createElement('button');
      b.textContent = t('暂停');
      b.onclick = ()=>aiMissionsOp('pause', m.id);
      acts.appendChild(b);
    }
    if(open){
      const c = document.createElement('button');
      c.textContent = t('取消'); c.className = 'danger';
      c.onclick = ()=>aiMissionsOp('cancel', m.id);
      acts.appendChild(c);
    }
    row.appendChild(acts);
    list.appendChild(row);
  });
}

function aiMissionsOp(op, id){
  // 正在跑的行动被暂停/取消：先掐断流，让服务端 settle 按中断收口（落 paused）
  if(aiCurMission === id && aiCtrl){ try{ aiCtrl.abort(); }catch(_){} }
  fetch('/api/missions', {method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({op: op, id: id})})
    .then(r=>r.json()).then(d=>{
      if(!d || !d.ok){ toast((d && d.error) || t('操作失败'), 'err'); return; }
      // 本地立刻更新（SSE 会再推一次全量，两相对齐）
      if(aiMissionRows){
        const i = aiMissionRows.findIndex(x=>x.id === id);
        if(i >= 0 && d.mission) aiMissionRows[i] = d.mission;
        aiMissionsRender();
      }
      if(op === 'cancel' && aiCurMission === id) aiCurMission = null;
    }).catch(()=>toast(t('读取失败'), 'err'));
}

function aiMissionsResume(m){
  if(aiBusy){ toast(t('助手正在忙，稍后再试'), 'err'); return; }
  aiSide('trace');
  // 绑定交给 aiSendText 第二参；消息内容只是"继续"，
  // 真正的上下文（目标/已完成步骤/断点）由服务端注入 system（定案 3）
  aiSendText(t('继续执行该行动'), m.id);
}

function aiMissionsCreate(){
  const input = $ai('ai-ms-goal');
  const goal = String((input && input.value) || '').trim();
  if(!goal){ if(input) input.focus(); return; }
  if(aiBusy){ toast(t('助手正在忙，稍后再试'), 'err'); return; }
  fetch('/api/missions', {method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({op:'create', goal: goal})})
    .then(r=>r.json()).then(d=>{
      if(!d || !d.ok){ toast((d && d.error) || t('操作失败'), 'err'); return; }
      if(input) input.value = '';
      // 手动新建 = 立即走 AI 对话通道执行（设计 2.3）
      aiSide('trace');
      aiSendText(goal, d.mission && d.mission.id);
    }).catch(()=>toast(t('读取失败'), 'err'));
}

/* ---------- 文件引用：只产出路径，不读内容 ---------- */
function aiChipsRender(){
  const box = $ai('ai-chips'); if(!box) return;
  box.textContent = '';
  box.hidden = !aiFiles.length;
  aiFiles.forEach((f, i)=>{
    const c = document.createElement('span'); c.className = 'ai-chip';
    const b = document.createElement('b'); b.textContent = f.name; b.title = f.path;
    const x = document.createElement('i'); x.textContent = '×'; x.title = t('移除');
    x.onclick = ()=>{ aiFiles.splice(i, 1); aiChipsRender(); };
    c.appendChild(b); c.appendChild(x);
    box.appendChild(c);
  });
}
function aiFileRef(){
  if(!aiFiles.length) return '';
  const lines = aiFiles.map(f=>'- ' + f.path).join('\n');
  aiFiles = []; aiChipsRender();
  return t('【附加文件引用】') + '\n' + lines + '\n' + t('（需要内容时用 read_file 读取；沙箱外路径会被拒绝）') + '\n\n';
}
function aiJoin(dir, name){
  if(!dir) return name;
  return (dir.slice(-1) === '\\' || dir.slice(-1) === '/') ? dir + name : dir + '\\' + name;
}
function aiParent(p){
  if(!p) return '';
  let s = p.replace(/[\\\/]+$/, '');
  const i = Math.max(s.lastIndexOf('\\'), s.lastIndexOf('/'));
  if(i < 0) return '';
  const up = s.slice(0, i);
  return /^[A-Za-z]:$/.test(up) ? up + '\\' : up;
}
function aiPickShow(on){
  const pop = $ai('ai-pick'); if(!pop) return;
  pop.classList.toggle('show', !!on);
  const b = $ai('ai-plus'); if(b) b.classList.toggle('on', !!on);
}
function aiPickClose(){ aiPickShow(false); }
function aiPickOpen(){
  aiPickShow(true);
  let p = '';
  try{ if(cfg && cfg.projects) p = cfg.projects.frontend_dir || cfg.projects.backend_dir || ''; }catch(_){}
  aiPickLs(p);
}
async function aiPickLs(p){
  const list = $ai('ai-pick-list'); if(!list) return;
  aiPickPath = p || '';
  const pv = $ai('ai-pick-path'); if(pv) pv.value = aiPickPath || t('驱动器');
  list.textContent = t('读取中…');
  let j = null;
  try{ j = await (await fetch('/api/ls?mode=file&path=' + encodeURIComponent(aiPickPath))).json(); }
  catch(e){ list.textContent = t('读取失败'); return; }
  list.textContent = '';
  const add = (ic, name, fn, title)=>{
    const d = document.createElement('div'); d.className = 'ai-pick-item';
    const i = document.createElement('i'); i.className = 'ic'; i.textContent = ic;
    const s = document.createElement('span'); s.textContent = name; if(title) s.title = title;
    d.appendChild(i); d.appendChild(s); d.onclick = fn;
    list.appendChild(d);
  };
  if(j.error){ const e = document.createElement('div'); e.className = 'muted'; e.style.padding = '6px'; e.textContent = j.error; list.appendChild(e); }
  if(!aiPickPath){
    (j.drives || []).forEach(dr=>add('▣', dr, ()=>aiPickLs(dr)));
    return;
  }
  if(j.parent) add('↑', t('上级目录'), ()=>aiPickLs(j.parent));
  (j.dirs || []).forEach(n=>add('▸', n, ()=>aiPickLs(aiJoin(aiPickPath, n))));
  (j.files || []).forEach(n=>add('·', n, ()=>aiPickAdd(aiJoin(aiPickPath, n), n)));
  if(!(j.dirs || []).length && !(j.files || []).length){
    const e = document.createElement('div'); e.className = 'muted'; e.style.padding = '6px'; e.textContent = t('这个目录是空的'); list.appendChild(e);
  }
}
function aiPickAdd(p, name){
  if(aiFiles.some(f=>f.path === p)){ toast(t('已经加过了')); return; }
  aiFiles.push({path: p, name: name || p.split(/[\\\/]/).pop()});
  aiChipsRender(); aiPickClose();
  toast(t('已附加引用：{0}', name || p));
}
async function aiDropFiles(files){
  const MAX = 64 * 1024;
  const ta = $ai('ai-in'); if(!ta) return;
  for(const f of files){
    if(f.size > MAX){ toast(t('文件超过 64KB，已跳过：{0}', f.name), 'err'); continue; }
    let txt = '';
    try{ txt = await f.text(); }catch(e){ toast(t('读取失败：{0}', f.name), 'err'); continue; }
    // 与项目既有口径一致：外部文件内容一律当**不可信数据**包裹，不让其中的指令生效
    ta.value += '\n<untrusted_data source="file:' + f.name + '">\n' + txt + '\n</untrusted_data>\n';
    toast(t('已读入：{0}', f.name));
  }
  ta.focus();
}
/* 导出本轮：给答辩/工单留一份可读证据，轨迹一起带走 */
function aiExportTurn(d){
  const ts = d.dataset.ts || aiNow();
  const body = d.dataset.raw || d.innerText || '';
  const nodes = Array.isArray(d.__trace) ? d.__trace : [];
  let md = '# EnvKit 对话导出 · ' + ts + '\n\n' + body + '\n\n';
  if(nodes.length){
    md += '## 轨迹\n\n';
    nodes.forEach(n=>{
      md += '- [' + aiClock(n.at) + '] ' + (AI_KIND[n.kind] || n.kind) + '：' + n.text + '\n';
      if(n.detail) md += '  ```\n  ' + n.detail.replace(/\n/g, '\n  ') + '\n  ```\n';
    });
  }
  const blob = new Blob([md], {type: 'text/markdown;charset=utf-8'});
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'envkit-chat-' + Date.now() + '.md';
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(()=>URL.revokeObjectURL(a.href), 3000);
  toast(t('已导出本轮'));
}

/* 请求体被中间层丢弃时的提示卡（v3.2）。
   为什么不只打一行灰字：这件事的后果是"助手忘事 + 缓存命中率封顶"，
   用户看不见原因、只会觉得模型变笨。所以给一张能关掉的卡，附上修法。
   同一会话最多提示 3 次——第 4 次开始只是噪音。 */
let aiBodyWarnN = 0;
function aiBodyWarn(n){
  if(++aiBodyWarnN > 3) return;
  const box = document.createElement('div'); box.className = 'ai-warn';
  const h = document.createElement('b'); h.textContent = t('⚠ 请求内容被中间层丢弃');
  box.appendChild(h);
  const p = document.createElement('div');
  p.textContent = t('本次只恢复了最近 {0} 条消息，历史可能不完整——这会让助手"忘事"，也会让缓存命中率封顶。', n);
  box.appendChild(p);
  const det = document.createElement('details');
  const sm = document.createElement('summary'); sm.textContent = t('最常见原因与修法');
  const fix = document.createElement('div'); fix.className = 'ai-warn-fix';
  fix.textContent = t('1) 本机代理（Clash / V2Ray 等，常见 127.0.0.1:7890）没有绕过 localhost：\n   在代理的「绕过 / 例外 / Bypass」里加入 127.0.0.1;localhost;::1\n2) 或换一个不走系统代理的浏览器打开本控制台\n\n验证：到「AI 助手」页点「检测请求传输」，显示"发送 N / 收到 N"即已修好。');
  det.appendChild(sm); det.appendChild(fix); box.appendChild(det);
  $ai('ai-msgs').appendChild(box);
  $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
}

/* 确认卡的决定：**先走原地送达**（v3.2），失败才退回旧路径。
   原地送达 = 服务端还在同一个 SSE 流里等着，决定一到就接着执行；
   旧路径 = 带 confirm 重新提交，等于把上下文整体重建一次（贵，且把任务切成两段）。 */
async function aiDecide(approved){
  const p = aiPending; if(!p) return;
  aiPending = null;
  try{ p.card.remove(); }catch(_){}
  petConfirmHide();
  aiAdd('sys', approved ? t('已确认，继续执行…') : t('已拒绝，让助手换个做法…'));
  aiTraceAdd('human', approved ? t('你已确认：{0}', p.tool) : t('你已拒绝：{0}', p.tool), {cls: approved ? 'ok' : 'warn'});
  aiHstat(approved ? t('执行中') : t('已拒绝'));
  // 批量授权只认"确认"这一次：拒绝时即使勾了也不开
  const wantBatch = !!(approved && p.batchOf && p.batchOf());
  try{
    const r = await fetch('/api/ai/confirm', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({tool_call_id: p.id, tool: p.tool, args: p.args || {},
        approved: !!approved, batch: wantBatch})
    });
    // **不能只看 r.ok**：旧版服务端没有这个路由时会落到 "/" 的页面处理器，
    // 同样回 200（一段 HTML）。只认 JSON 里的 ok:true，否则一律当"没送达"走旧路径。
    if(r.ok){
      const j = await r.json().catch(()=>null);
      if(j && j.ok) return;   // 同一个流会继续吐 delta / tool_result / usage
    }
  }catch(_){}
  // 兜底：404=没有等待者（旧版本、已超时、或卡来自旧路径）→ 走回原路。
  // 这里原来是"abort → 睡 150ms 等旧流的 finally 复位 aiBusy → 不 await 地开新流"。
  // 150ms 是竞态赌注：旧流 finally 晚于它执行就会把新流刚设的 aiBusy/按钮态冲掉。
  // v3.3 起 aiConsume 有代际计数（aiGen）：新流一开跑，旧流的 finally 就无权复位，
  // 睡眠可以整个删掉。
  if(aiCtrl){ try{ aiCtrl.abort(); }catch(_){} }
  await aiConsume('/api/ai/chat', {messages: aiMessages,
    confirm: {tool_call_id: p.id, tool: p.tool, args: p.args || {}, approved: !!approved, batch: wantBatch}});
}

/* ---------- 绑定 ---------- */
(function aiBindUI(){
  const on = (id, ev, fn)=>{ const el = document.getElementById(id); if(el) el.addEventListener(ev, fn); };
  on('ai-expand', 'click', ()=>aiBig(!aiBigOn));
  on('ai-trace-open', 'click', ()=>aiSide(aiSideTab || 'trace'));
  document.querySelectorAll('#ai-tabs button').forEach(b=>b.addEventListener('click', ()=>aiSide(b.dataset.tab)));
  on('ai-plus', 'click', ()=>{ const pop = $ai('ai-pick'); if(pop && pop.classList.contains('show')) aiPickClose(); else aiPickOpen(); });
  on('ai-pick-close', 'click', aiPickClose);
  on('ai-pick-up', 'click', ()=>{ const up = aiParent(aiPickPath); aiPickLs(up || ''); });
  const msgs = $ai('ai-msgs');
  if(msgs){
    msgs.addEventListener('dragover', e=>{ e.preventDefault(); });
    msgs.addEventListener('drop', e=>{
      if(!e.dataTransfer || !e.dataTransfer.files || !e.dataTransfer.files.length) return;
      e.preventDefault();
      aiDropFiles(Array.prototype.slice.call(e.dataTransfer.files));
    });
  }
  // Esc：先关文件选择器，再关抽屉（避免误关正在输入的对话）
  document.addEventListener('keydown', e=>{
    if(e.key !== 'Escape') return;
    const pop = $ai('ai-pick');
    if(pop && pop.classList.contains('show')){ aiPickClose(); e.stopPropagation(); return; }
    const dr = $ai('ai-drawer');
    if(dr && dr.classList.contains('open') && !aiBusy){ dr.classList.remove('open'); petShift(false); }
  });
})();

/* 服务商与模型 */
let aiPresets = [];
let aiPulledModels = [];   // 「拉取模型列表」得到的模型（**优先于静态预设**）
let aiPulledFrom = '';     // 上面那份列表来自哪个 Base URL（改动后即作废）
async function aiLoadProviders(){
  try{ aiPresets = (await (await fetch('/api/ai/providers')).json()).providers || []; }catch(e){ aiPresets = []; }
  const sel = $ai('ai_provider');
  // 「自定义」由后端预置表统一给出（带 icon），**前端不再追加**——
  // 2026-10-23 用户截图发现两个「自定义」：后端列表已含 custom 项，前端又硬拼了一个。
  sel.innerHTML = aiPresets.map(p=>`<option value="${p.id}">${p.name}</option>`).join('');
  // 自绘下拉菜单：原生 select 的 option 渲染不了图片，官方品牌图标走这里
  const menu = $ai('ai_provider_menu');
  menu.innerHTML = aiPresets.map(p=>{
    const ic = aiProvIconHTML(p.id);
    return `<div class="prov-item" data-id="${p.id}" role="option">${ic}<span>${p.name}</span></div>`;
  }).join('');
  menu.querySelectorAll('.prov-item').forEach(it=>{
    it.addEventListener('click', ()=>{
      menu.style.display = 'none';
      if(sel.value !== it.dataset.id){
        sel.value = it.dataset.id;
        sel.dispatchEvent(new Event('change'));   // 复用既有 change 链：清拉取缓存 + 应用预置 + 自动保存
      }
    });
  });
  sel.value = 'deepseek';
  aiProviderDDSync();
}
// 图标渲染：内联 SVG（tools/brand_icons_build.py 注入的 AI_PROV_ICONS）；
// GLM 官方矢量没拿到，用品牌色徽章兜底
function aiProvIconHTML(id){
  const ic = (typeof AI_PROV_ICONS !== 'undefined') && AI_PROV_ICONS[id];
  if(ic) return `<svg viewBox="${ic.vb}" fill="${ic.fill}" aria-hidden="true">${ic.svg}</svg>`;
  if(id === 'glm') return '<span class="picon-z">Z</span>';
  return '';
}
// 按钮态（当前选中项的图标+名字）与菜单高亮跟随 select.value——
// aiApplyProvider 会在每次值变化后被调，同步挂这里最省心
function aiProviderDDSync(){
  const sel = $ai('ai_provider');
  const p = aiPresets.find(x=>x.id === sel.value);
  $ai('ai_provider_cur_name').textContent = p ? p.name : (sel.value || '');
  $ai('ai_provider_cur_ic').innerHTML = aiProvIconHTML(sel.value);
  $ai('ai_provider_menu').querySelectorAll('.prov-item').forEach(it=>{
    it.classList.toggle('on', it.dataset.id === sel.value);
  });
}
(function(){
  const btn = $ai('ai_provider_btn'), menu = $ai('ai_provider_menu');
  btn.addEventListener('click', e=>{
    e.stopPropagation();
    menu.style.display = menu.style.display === 'none' ? '' : 'none';
  });
  menu.addEventListener('click', e=>e.stopPropagation());
  document.addEventListener('click', ()=>{ menu.style.display = 'none'; });
})();
function aiProviderPreset(){ return aiPresets.find(p=>p.id === $ai('ai_provider').value) || null; }
function aiApplyProvider(keepModel){
  const p = aiProviderPreset();
  const custom = !p || p.id === 'custom';
  if(!custom && p.base_url) $ai('ai_base').value = p.base_url;
  $ai('ai_base').readOnly = !custom;
  const sel = $ai('ai_model_sel');
  // **拉取到的模型优先于静态预设。**
  //
  // 原来写的是 `custom ? aiPulledModels : (p.models || [])`——
  // 非自定义服务商下**把拉取结果丢掉了**，于是下拉框显示的仍是预设。
  // 实测踩过：厂商停用deepseek-chat / deepseek-reasoner 之后，
  // 用户点了「拉取模型列表」，界面明确提示「已拉取 2 个模型，已在下拉框中回填」，
  // 而下拉框里**仍然是那两个失效名字**。
  //
  // **提示说回填了、实际没回填** —— 这比不回填更坏：
  // 用户会以为列表是厂商的真实在售模型，实际看到的是软件自己的过期快照。
  // 预设是硬编码的静态列表，厂商改名后它不会自己更新；
  // 拉取到的才是真值。**有真值就该用真值。**
  //
  // 预设仍作为**拉取失败时的兜底**（无网络、Key 错、服务商没实现 /models）。
  // 拉取结果只在「有列表 且 地址没变」时才算数。
  // 地址变了还沿用旧列表，会让用户挑一个这个地址上并不存在的模型。
  const sameAddr = aiPulledFrom === $ai('ai_base').value.trim();
  const pulled = (aiPulledModels && aiPulledModels.length && sameAddr) ? aiPulledModels : [];
  const models = pulled.length ? pulled : (p && p.models) || [];
  const useSelect = !custom || models.length > 0;
  sel.style.display = useSelect ? '' : 'none';
  $ai('ai_model_row').style.display = useSelect ? 'none' : '';
  const current = keepModel || '';
  sel.innerHTML = models.map(m=>`<option value="${m}">${m}</option>`).join('') + '<option value="__custom__">自定义模型名...</option>';
  if(current){
    if([...sel.options].some(o=>o.value===current)) sel.value = current;
    else { const o = document.createElement('option'); o.value = current; o.textContent = current; sel.prepend(o); sel.value = current; }
  }
  if(!useSelect) $ai('ai_model_row').style.display = '';
  // 服务商说明（预设的 Note）——2026-10-17 起 DeepSeek 不再带说明：
  // 用户反馈「这句话不需要看」。机制保留：哪个服务商确需说明就在后端加 Note，
  // 走 t() 翻译（服务端直出中文会让英文界面露中文）；没有 Note 时整行隐藏。
  const hint = $ai('ai_provider_note');
  if(hint){
    const raw = (p && !custom && p.note) ? p.note : '';
    const txt = raw ? t(raw) : '';
    hint.querySelector('span').textContent = txt;
    hint.style.display = txt ? '' : 'none';
  }
  aiProviderDDSync();   // 自绘下拉的按钮图标/名字/菜单高亮跟随当前值
}
// 换服务商等于换地址，上一个地址的模型列表必须一起作废
$ai('ai_provider').addEventListener('change', ()=>{ aiPulledModels = []; aiPulledFrom = ''; aiApplyProvider(''); aiScheduleAutoSave(); });
$ai('ai_model_sel').addEventListener('change', ()=>{
  const isCustom = $ai('ai_model_sel').value === '__custom__';
  $ai('ai_model_row').style.display = isCustom ? '' : 'none';
  if(isCustom){ $ai('ai_model_sel').style.display = 'none'; setTimeout(()=>$ai('ai_model').focus(), 30); }
});
function aiCurrentModel(){
  if($ai('ai_model_sel').style.display === 'none' || $ai('ai_model_sel').value === '__custom__') return $ai('ai_model').value.trim();
  return $ai('ai_model_sel').value;
}

/* 自动保存/自动拉取链的状态——必须声明在 aiLoadConfig 之前：
   aiLoadConfig 在页面初始化（ekSetLang）就被调用，而 let 有暂时性死区，
   声明放在它后面会炸 "Cannot access 'aiSuppress' before initialization"，
   连锁炸断整个 AI 设置卡片初始化（下拉空白、启用开关失灵）。 */
let aiAutoTimer = null;      // 防抖句柄
let aiAutoBusy = false;      // 上一次自动链还没跑完时，新改动只重置计时器
let aiKeySet = false;        // 后端是否已存有 Key（决定自动拉取要不要发起）
let aiSuppress = false;      // 程序化回填（加载配置/切换服务商）期间不触发自动保存

/* 设置卡片 */
async function aiLoadConfig(){
  try{
    aiSuppress = true;   // 程序化回填不触发自动保存（change/input 事件都会被它拦下）
    const c = await (await fetch('/api/ai/config')).json();
    await aiLoadProviders();
    $ai('ai_provider').value = c.provider || 'deepseek';
    if([...$ai('ai_provider').options].every(o=>o.value!==c.provider)){ $ai('ai_provider').value = 'custom'; }
    aiApplyProvider(c.model || '');
    $ai('ai_base').value = c.base_url || $ai('ai_base').value;
    $ai('ai_enabled').checked = !!c.enabled;
    $ai('ai_memory_on').checked = !!c.memory_enabled;
    aiKeySet = !!c.key_set;   // 自动拉取要用：后端已存 Key 才值得发 /models 请求
    $ai('ai_key').placeholder = c.key_set ? t('已保存（{0}），留空保留', c.key_masked||'****') : 'sk-...';
    $ai('ai_temp').value = (c.temperature === undefined || c.temperature === null) ? '' : (Math.round(Number(c.temperature)*100)/100);
    const q = c.quirk || {};
    const notes = [];
    if(q.temp_mode === 'one') notes.push(t('temperature 固定为 1'));
    if(q.temp_mode === 'none') notes.push(t('不发送 temperature（上游不接受）'));
    if(q.max_tokens_field === 'max_completion_tokens') notes.push(t('使用 max_completion_tokens'));
    if(q.no_tools) notes.push(t('该模型不支持工具调用（已降级为纯问答）'));
    $ai('ai_quirk').textContent = notes.join('；');
    $ai('ai_quirk_row').style.display = notes.length ? '' : 'none';
  }catch(e){}
  finally{ aiSuppress = false; }   // 无论成功失败都要放行，否则自动保存永久失灵
}
aiLoadConfig();
$ai('btn-ai-models').onclick = async ()=>{
  $ai('h-ai').textContent = t('拉取模型列表中...');
  try{
    const c = await (await fetch('/api/ai/models', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({base_url:$ai('ai_base').value.trim()})})).json();
    if(!c.ok){ $ai('h-ai').textContent = aiPullErrHuman(c.error); return; }
    const cur = aiCurrentModel();
    aiPulledModels = c.models || [];
    // 记录这份列表来自哪个地址：**Base URL 改动后必须作废**，
    // 否则会出现「上一个服务商的模型名配着新地址显示」——
    // 用户挑了一个这个地址上并不存在的模型，保存后才发现。
    aiPulledFrom = $ai('ai_base').value.trim();
    aiApplyProvider(cur);   // 重新渲染：下拉框显示拉取到的真值并选中当前模型
    $ai('h-ai').textContent = t('已拉取 {0} 个模型，已在「模型」下拉框中回填', aiPulledModels.length);
  }catch(e){ $ai('h-ai').textContent = aiPullErrHuman(e); }
};
$ai('btn-ai-test').onclick = async ()=>{
  $ai('h-ai').textContent = t('测试中...');
  try{
    const c = await (await fetch('/api/ai/test', {method:'POST'})).json();
    $ai('h-ai').textContent = c.ok ? (t('连接正常 · ')+c.latency_ms+'ms') : (t('失败：')+(c.error||t('未知')));
  }catch(e){ $ai('h-ai').textContent = t('失败：')+e; }
};

/* 传输自检（v3.3）：**两种发法各发一次**，用来分辨"到底是哪一部分被中间层吃了"。
   背景：自检（纯 body）能完整到达，而真实的 /api/ai/chat 请求会「头到了、body 0 字节」。
   两者只差三样东西：大自定义头（对话兜底头）、AbortSignal、SSE 流式响应。
   所以这里分别发：
     A 普通：只有 body；
     B 带对话头：body 一样大，但附上和真实请求同尺寸的 X-EnvKit-Chat-Fallback。
   A 通过、B 失败 → 就是那个大头惹的祸（那它就不该继续出现在热路径上）；
   两个都通过 → 问题在信号/流式那一侧，继续往那边查。 */
const _btnTransport = $ai('btn-ai-transport');
if(_btnTransport) _btnTransport.onclick = async ()=>{
  const h = $ai('h-ai');
  h.textContent = t('正在检测…');
  const mk = ()=>{
    const raw = new Uint8Array(4096);
    crypto.getRandomValues(raw);
    let s = '';
    for(let i = 0; i < raw.length; i++) s += String.fromCharCode(raw[i] & 0x7f);
    return JSON.stringify({probe: btoa(s)});
  };
  const probe = async (withChatHeader)=>{
    const bodyStr = mk();
    const sent = new TextEncoder().encode(bodyStr).length;
    const headers = {'Content-Type': 'application/json', 'X-EnvKit-Body-Len': String(sent)};
    if(withChatHeader){
      // 造一个和真实对话同量级（约 6 条 × 200 字）的兜底头
      const msgs = [];
      for(let i = 0; i < 6; i++) msgs.push({r: i % 2 ? 'assistant' : 'user', c: '这是用于测量请求头体积的一段中文内容'.repeat(5)});
      headers['X-EnvKit-Chat-Fallback'] = encodeURIComponent(JSON.stringify({last: 'x', recent: msgs, lang: 'zh'}));
    }
    try{
      const r = await fetch('/api/ai/echo', {method:'POST', headers, body: bodyStr});
      const j = await r.json();
      return {sent, got: (j && j.len) || 0, ok: !!(j && j.ok && j.len === sent), headerLen: (headers['X-EnvKit-Chat-Fallback'] || '').length};
    }catch(e){ return {sent, got: -1, ok: false, err: String(e), headerLen: (headers['X-EnvKit-Chat-Fallback'] || '').length}; }
  };
  const a = await probe(false);
  const b = await probe(true);
  const fmt = (x)=> x.got < 0 ? t('请求失败') : t('发送 {0} / 收到 {1}', x.sent, x.got);
  if(a.ok && b.ok){
    h.textContent = t('传输正常（普通 {0}；带对话头 {1}，头长 {2} 字节）——问题不在体积或请求头上', fmt(a), fmt(b), b.headerLen);
  } else if(a.ok && !b.ok){
    h.textContent = t('普通请求正常（{0}），但带对话头的那次失败（{1}，头长 {2} 字节）—— 中间层在处理大请求头时丢掉了 body', fmt(a), fmt(b), b.headerLen);
  } else {
    h.textContent = t('连普通请求都不完整（{0}）—— 传输链路本身有问题', fmt(a));
  }
};

/* ---------- 表单自动保存 + 自动拉取模型（2026-10-23） ----------
 *
 * 用户裁定的新主链路：**改完即存、存完即拉**。
 * 模型名的真值永远来自服务商的 /models（内置预置只是拉取失败时的兜底），
 * 所以把「保存配置 → 拉取模型列表」从两个手动按钮改成一条自动链：
 * 选服务商（URL 自动回填）→ 填 Key → 防抖自动保存 → 自动拉取回填。
 * 拉取成功 = Key 和网址都对（当场验证，不用再点「测试连接」）；
 * 拉取失败 = 错误原样亮出来，用户立刻知道是 Key 错还是地址错。
 */

function aiCollectPayload(){
  const model = aiCurrentModel();
  if(!model) return null;    // 模型还没选（比如刚切服务商正在拉取），这轮不存
  return {enabled:$ai('ai_enabled').checked, memory_enabled:$ai('ai_memory_on').checked,
    provider:$ai('ai_provider').value, base_url:$ai('ai_base').value.trim(),
    api_key:$ai('ai_key').value.trim(), model,
    temperature: ($ai('ai_temp').value.trim() === '' ? null : Number($ai('ai_temp').value))};
}

/* 自动保存状态字：静默保存也要让用户看见「存没存上」——
   2026-10-23 用户把「保存链炸了」误判成「启用开关失灵」，根因就是全程无反馈。 */
function aiAutoSaveSt(txt, bad){
  const el = $ai('ai_autosave_st');
  if(!el) return;
  el.textContent = txt ? t(txt) : '';
  el.classList.toggle('bad', !!bad);
}

/* 拉取失败的人话分类：超时 / 401 / 404 / DNS 各是各的病，
   别让用户对着 Go 的报错原文猜。命中不了就原样透出（不编造）。 */
function aiPullErrHuman(err){
  err = String(err||'');
  // 后端对「地址≠已存服务商」的 401 有专属说明（防密钥发往陌生域名没带 Key），
  // 这个要先于「Key 被拒」判断，否则会把「没带 Key」误说成「Key 错」
  if(err.indexOf('未附带 API Key') >= 0)
    return t('该地址与已保存的服务商不一致，为防密钥外发未附带 Key——先保存配置再拉取');
  const m = err.match(/HTTP (\d+)/);
  if(m){
    const code = m[1];
    if(code === '401' || code === '403') return t('Key 被拒（HTTP {0}）：检查 API Key 是否正确', code);
    if(code === '404') return t('模型地址不存在（HTTP 404）：检查 Base URL 是否少了 /v1 或多了路径');
    return t('服务商返回 HTTP {0}', code) + '：' + err;
  }
  if(/Client\.Timeout|context deadline/i.test(err)) return t('连接超时：服务商无响应或网络不通');
  if(/no such host|lookup/i.test(err)) return t('域名无法解析：检查 Base URL 拼写与网络');
  if(/connection refused/i.test(err)) return t('连接被拒：地址或端口不对');
  if(/x509|certificate|tls:/i.test(err)) return t('HTTPS 证书异常：检查地址与系统时间');
  return t('拉取失败：') + err;
}

async function aiPullModelsCore(){
  // 拉取前必须已存 Key（/models 用后端 DPAPI 里的 Key 做 Bearer）；
  // 没有就跳过——让用户先填 Key，免得每次都吃一个注定 401 的请求。
  if(!aiKeySet && !$ai('ai_key').value.trim()) return;
  try{
    const c = await (await fetch('/api/ai/models', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({base_url:$ai('ai_base').value.trim()})})).json();
    if(!c.ok){ $ai('h-ai').textContent = t('自动保存完成，但拉取模型失败：')+aiPullErrHuman(c.error); return false; }
    const cur = aiCurrentModel();
    aiPulledModels = c.models || [];
    aiPulledFrom = $ai('ai_base').value.trim();   // Base URL 改动后旧列表作废（同手动拉取）
    aiApplyProvider(cur);
    $ai('h-ai').textContent = t('已自动保存，已拉取 {0} 个模型并回填', aiPulledModels.length);
    return true;
  }catch(e){ $ai('h-ai').textContent = t('自动保存完成，但拉取模型失败：')+aiPullErrHuman(e); return false; }
}

async function aiAutoSaveNow(){
  const payload = aiCollectPayload();
  if(!payload) return;
  aiAutoBusy = true;
  aiAutoSaveSt('保存中…');
  try{
    const r = await fetch('/api/ai/config', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(payload)});
    if(!r.ok){ aiAutoSaveSt('保存失败：HTTP '+r.status, true); return; }
    if(payload.api_key){ $ai('ai_key').value=''; aiKeySet = true;
      // Key 已入库，刷新脱敏占位符（只动 placeholder，不整表重载——整表重载会打断用户正在输入的字段）
      try{ const c = await (await fetch('/api/ai/config')).json();
        if(c.key_set) $ai('ai_key').placeholder = t('已保存（{0}），留空保留', c.key_masked||'****');
      }catch(e){}
    }
    memSyncGate();
    await aiPullModelsCore();
    aiAutoSaveSt('已自动保存 ✓');   // 拉取的成败在 h-ai 里说，这里只报保存结果
  }finally{ aiAutoBusy = false; }
}

function aiScheduleAutoSave(){
  if(aiSuppress) return;
  aiAutoSaveSt('有改动，待保存…');
  clearTimeout(aiAutoTimer);
  aiAutoTimer = setTimeout(()=>{ if(!aiAutoBusy) aiAutoSaveNow(); else aiScheduleAutoSave(); }, 900);
}

// 绑定自动保存：下拉/开关用 change，文本输入用 input（防抖合并击键）
// provider 的 change 在上面既有绑定里处理（清拉取缓存 + 应用预置），这里只挂保存调度
['ai_base','ai_key','ai_temp'].forEach(id=>$ai(id).addEventListener('input', aiScheduleAutoSave));
['ai_enabled','ai_memory_on'].forEach(id=>$ai(id).addEventListener('change', aiScheduleAutoSave));

/* 对话消费：读取 SSE 流 */
const AI_TOOL_CN = {get_system_state:'读取环境状态', get_logs:'读取日志', get_diag_report:'生成诊断报告',
  run_detection:'重新检测组件', db_backup:'备份数据库', start_service:'启动服务', stop_service:'停止服务',
  restart_service:'重启服务', apply_whitelist:'加入 Defender 白名单', db_check:'检测数据库连接',
  cleanup_processes:'清理后台进程', get_project_brief:'读取项目画像', list_project:'查看项目目录',
  search_files:'搜索项目文件', read_file:'读取项目文件', verify_environment:'复验环境状态',
  sync_chain_ip:'同步链地址到 config.toml',
  // v3.3 补漏：以下 9 个工具后端注册表里真实存在，此前没进这张表 ——
  // 中文界面上它们会以英文裸名出现在轨迹/确认卡里（"→ db_query"）。
  db_query:'只读查询业务数据库', db_list:'列出数据库与表',
  get_audit_log:'读取审计日志', list_backups:'列出备份',
  get_env_snapshot:'读取环境快照', check_env_req:'检查项目版本要求',
  get_chain_guard:'读取链守护状态', recall_lessons:'检索经验教训',
  manage_memories:'管理长期记忆'};
function aiToolName(name){ return t(AI_TOOL_CN[name] || name) + '（' + name + '）'; }

// v2.9：把 verify_environment 的客观结果渲染成状态徽章。
//
// ## 为什么它不是「又一个tool_result」
//
// tool_result 展示的是**工具返回的文本**，而那段文本是写给模型读的指令
// （「必须如实告诉用户还没确认成功」之类）。用户看到的是**模型对它的转述**——
// 而转述可能说反。这正是「执行即成功」在展示层的重演。
//
// 徽章只认 SSE 里的 ok/verified 两个布尔值，
// 所以 AI 说得再漂亮也改不了它的颜色。
//
//## 三态必须三色
//
//|状态 | 含义 | 为什么不能合并 |
// |---|---|---|
// | ok && verified | 真的恢复了 | — |
// | ok && !verified | 动作执行了，但**没证据说恢复了** | 与「恢复成功」合并 = 白做 |
// | !ok | 复验本身失败 | 与上面两种合并 = 又变成一句「已处理」 |
function aiRenderVerify(ev){
  // **cls 与 badge 由 Go 侧算好后传过来**（aiVerifyBadgeClass/Text）。
  // 这里只做映射，不重新判断三态 —— 两处独立判断会各自演化，
  // 然后徽章就开始显示错误状态，而它唯一的价值就是「它说验了就是真验了」。
  // 兜底仍按 ok/verified 算，只是为了后端旧版本时不至于空白。
  const cls = ev.cls || (function(){ const ok=!!ev.ok, vf=!!ev.verified;
    return !ok ? 'is-bad' : (vf ? 'is-ok' : 'is-pending'); })();
  // 徽章文案**必须走 t()** —— Go 侧传来的 badge 是中文，
  // 而界面可能切到英文。直接显示会让英文界面里混进中文。
  // 所以按三态重新查表，Go 侧的 badge 只作兜底。
  const badge = t(!ev.ok ? '复验未通过' : (ev.verified ? '已复验通过' : '已执行·未确认'))
    || ev.badge || '';
  const d = document.createElement('div');
  d.className = 'ai-verify ' + cls;
  const head = document.createElement('div'); head.className = 'v-head';
  const b = document.createElement('span'); b.className = 'v-badge'; b.textContent = badge;
  head.appendChild(b);
  /* 归因要用后端专门传来的 ev.tool：v3.0 让 start/restart_service 的
     "启动即复验"也发 verify_result 并带上 tool 字段，就是为了让下游分辨
     这条结论是哪次动作产生的。写死 verify_environment 会把归因丢掉
     （用户看到「复验环境：已复验通过」，其实是「重启后端」的复验）。 */
  const tgt = document.createElement('span');
  tgt.textContent = aiToolName(ev.tool || 'verify_environment').replace(/（[^）]*）$/, '');
  head.appendChild(tgt);
  if(ev.msg){
    const m = document.createElement('span'); m.className = 'v-msg'; m.textContent = ev.msg;
    head.appendChild(m);
  }
  d.appendChild(head);
  const evs = Array.isArray(ev.evidence) ? ev.evidence.filter(Boolean) : [];
  if(evs.length){
    const det = document.createElement('details');
    const sm = document.createElement('summary'); sm.textContent = t('查看证据');
    det.appendChild(sm);
    const pre = document.createElement('pre'); pre.className = 'v-ev'; pre.textContent = evs.join('\n');
    det.appendChild(pre);
    d.appendChild(det);
  }
  $ai('ai-msgs').appendChild(d);
  $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
}

let aiCtrl = null;      // AbortController：手动"停止生成"用
// aiGen 代际计数：abort 旧流后立刻开新流（确认投递失败的兜底路径就是这样），
// 旧流的 finally 若晚于新流执行，会把新流刚设的 aiBusy/按钮态冲掉——
// 流式进行中输入框又可再发一条，两条流交错往同一容器贴消息。
// 规则：finally 里先比对代际，不是当前代就整段跳过复位。
let aiGen = 0;
let aiPending = null;   // {asst, card}：待确认的写操作卡片。用户无视卡片直接发新消息时清理悬挂回合

async function aiConsume(path, body){
  const gen = ++aiGen;   // 本次流的代际：finally 里只有当代际匹配才允许复位全局态
  aiOpen(); aiBusy = true; $ai('ai-send').disabled = true; $ai('ai-stop').style.display = '';
  petAIState('thinking');   // 桌宠联动：立即进思考态（确认后续接轮也走这里，状态从 confirm → thinking）
  body = Object.assign({}, body || {}, {lang: EK_LANG});   // 让助手跟随界面语言作答
  // 行动绑定（Phase 1）：aiCurMission 非空时本回合是该行动的执行回合。
  // 放在 aiConsume 统一注入：确认续接轮（body.confirm）也自动带上，断点才接得回去。
  if(aiCurMission) body.mission_id = aiCurMission;
  // 回退头：某些本机代理/预览容器会剥掉 POST body（实测表现为服务端 decode 出 0 条消息，
  // 模型对着空输入自说自话）。令牌走请求头能完好穿过，所以把"最后一条用户消息 + 确认回执"
  // 也放进请求头兜底，body 丢失时服务端据此恢复。
  let fb = {last: '', recent: [], confirm: body.confirm || null, lang: EK_LANG};
  const ms = body.messages || [];
  for(let i = ms.length - 1; i >= 0; i--){ if(ms[i] && ms[i].role === 'user'){ fb.last = String(ms[i].content || '').slice(0, 2000); break; } }
  // 最近 6 条（每条截 200 字）：只带 last 的话，body 一旦被吃，模型等于失忆——
  // 它会重新问一遍（那些"菜单式无效回复"往往就是这么来的），这一轮缓存也全废。
  // 6×200 编码后约 3~4KB，仍在常见代理的头长限制内。
  fb.recent = ms.slice(-6).filter(m=>m && (m.role === 'user' || m.role === 'assistant'))
    .map(m=>({r: m.role, c: String(m.content || '').slice(0, 200)}));
  let cur = null, curText = '', curTs = '';
  // 触发本回合的那条用户原话——回合末尾工具栏的「重新提问」用它
  let reaskText = '';
  for(let i = aiMessages.length - 1; i >= 0; i--){ if(aiMessages[i] && aiMessages[i].role === 'user'){ reaskText = String(aiMessages[i].content || ''); break; } }
  let think = null, rsnText = '', rsnEl = null, renderQueued = false, rsnQueued = false;
  // v2.3 N5：计划面板。同一轮里所有步骤归到一个可折叠块内，
  // 结束后自动收起——用户中途能看，收完不占屏。
  let planEl = null, planList = null, planN = 0;
  const planBox = (maxTurns, reason)=>{
    if(planEl){ planEl.remove(); planEl = null; planList = null; }
    planN = 0;
    planEl = document.createElement('details');
    planEl.className = 'ai-plan';
    const sm = document.createElement('summary');
    sm.dataset.reason = reason || '';
    sm.textContent = t('执行计划（{0}）', reason ? reason + ' · ' + t('上限 {0} 轮', maxTurns) : t('上限 {0} 轮', maxTurns));
    planList = document.createElement('div');
    planList.className = 'ai-plan-list';
    planEl.appendChild(sm);
    planEl.appendChild(planList);
    $ai('ai-msgs').appendChild(planEl);
  };
  const planStep = (seq, text)=>{
    if(!planList){ planBox(0, ''); }
    const row = document.createElement('div');
    row.className = 'ai-plan-row';
    // 服务端数据必须 esc()：工具名/目标里可能有用户可控内容
    row.innerHTML = '<span class="ai-plan-n">' + esc(String(seq|| ++planN)) + '</span><span>' + esc(text||'') + '</span>';
    planList.appendChild(row);
    planN = Number(seq) || planN;
    $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
  };
  const planEnd = (text)=>{
    if(!planEl) return;
    // 收完自动折叠：步骤已经执行完，继续展开只是噪音
    planEl.open = false;
    const sm = planEl.querySelector('summary');
    if(sm && text) sm.title = text;
    planEl = null; planList = null;
  };
  // 提额：标题里的上限要跟着变，否则用户看到的还是那个已经作废的初值。
  // 标题上留一个可点开看原因的 title——不额外插一行，那个位置留给结论。
  const planExtend = (maxTurns, reason)=>{
    if(!planEl){
      planBox(maxTurns, reason);
      return;
    }
    const sm = planEl.querySelector('summary');
    if(!sm) return;
    sm.textContent = t('执行计划（{0}）', (sm.dataset.reason || '') + ' · ' + t('上限 {0} 轮', maxTurns));
    sm.title = reason || '';
    const row = document.createElement('div');
    row.className = 'ai-plan-row';
    row.innerHTML = '<span class="ai-plan-n">+</span><span style="color:var(--ok)">' +
      esc(t('每一轮都有新信息，上限自动放宽到 {0} 轮', maxTurns)) + '</span>';
    planList.appendChild(row);
    $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
  };
  try{
  // 思考气泡的构造函数。**单独抽出来是因为它要能被重建** ——
  // v2.3 起 `plan_start` 是服务端第一个事件，到达时就把气泡删了，
  // 而 `reasoning` 片段在它之后才到，不重建的话那段尾部渲染是死代码。
  // （v1.10.3 时 reasoning 能显示，正是因为没有 plan_start 抢删。）
  const mkThink = ()=>{
    if(think) return think;
    think = document.createElement('div'); think.className = 'ai-think';
    think.innerHTML = '<span>' + t('思考中') + '</span><span class="ai-dot"></span><span class="ai-dot"></span><span class="ai-dot"></span>';
    $ai('ai-msgs').appendChild(think);
    $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
    return think;
  };
  mkThink();
  const dropThink = ()=>{ if(think && think.parentNode){ think.remove(); think = null; } };
    aiCtrl = new AbortController();
    // 声明"我发了多少字节"：服务端据此判断请求体是**没到**还是**被截断**。
    // 长度按 UTF-8 字节算（不是 JS 的字符数，中文一个字 3 字节，用字符数会假报不符）。
    //
    // 发送前裁剪 + 剥冗余字段（v3.3）：aiMessages 只在**落盘**时截 60 条，内存里
    // 一个长会话是无限增长的——每轮都整体 JSON.stringify 两次（长度头 + body），
    // 请求体随会话线性膨胀。后端反正会裁到 40 条，客户端先裁到 60 条再发，
    // 省的是传输与两侧序列化的钱。ts 是前端本地渲染时间戳用的，后端 aiMsg
    // 没有该字段，纯浪费字节。tool_calls / reasoning_content 必须保留——
    // 思考型模型要求原样回传，确认续接路径靠 tool_calls 对齐。
    const _msgs = (Array.isArray(body.messages) ? body.messages : []).slice(-60).map(m => {
      const o = {role: m.role, content: String(m.content == null ? '' : m.content)};
      if(m.reasoning_content) o.reasoning_content = m.reasoning_content;
      if(m.tool_calls) o.tool_calls = m.tool_calls;
      if(m.tool_call_id) o.tool_call_id = m.tool_call_id;
      if(m.name) o.name = m.name;
      return o;
    });
    const _bodyStr = JSON.stringify(Object.assign({}, body, {messages: _msgs}));
    const _bodyLen = new TextEncoder().encode(_bodyStr).length;
    /* 整体超时兜底：后端的 aiTurnTimeout 是**每轮** 6 分钟，多轮任务叠加可以
       让界面转十几分钟，期间唯一逃生是手点「停止」。这里给整条请求设 15 分钟
       硬上限，到点自动 abort，界面至少会说出"连接中断"而不是永远三点动画。
       AbortSignal.any 在老浏览器不存在 → 手动联动两个信号。 */
    const _deadline = AbortSignal.timeout ? AbortSignal.timeout(15*60*1000) : null;
    const _sig = (_deadline && AbortSignal.any) ? AbortSignal.any([aiCtrl.signal, _deadline]) : aiCtrl.signal;
    if(_deadline && !AbortSignal.any){
      _deadline.addEventListener('abort', ()=>{ try{ aiCtrl.abort(); }catch(_){} }, {once:true});
    }
    const resp = await fetch(path, {method:'POST', signal: _sig, headers:{'Content-Type':'application/json', 'X-EnvKit-Body-Len': String(_bodyLen), 'X-EnvKit-Chat-Fallback': encodeURIComponent(JSON.stringify(fb))}, body: _bodyStr});
    if(!resp.ok){
      /* 后端失败时回的是 JSON（如 {"type":"error","text":"AI 助手未配置：…"}），
         直接把 resp.text() 甩到气泡里 = 用户看到一整行 JSON。
         解出 text 字段；结构对不上就退回到状态码，至少是句人话。 */
      let msg = 'HTTP ' + resp.status;
      try{ const j = await resp.json(); if(j && j.text) msg = j.text; }catch(_){}
      aiErr(msg); return;
    }
    const reader = resp.body.getReader(); const dec = new TextDecoder();
    let buf = '';
    // 流式渲染节流：每个 delta 全量 innerHTML 重排是 O(n²)，合并到每帧一次。
    // 流式期间**不做代码高亮**：每帧 innerHTML 重建会让高亮元素全部作废，
    // 等于每帧重跑一次 highlight，长回答直接卡死；收尾时统一高亮一次。
    let streamDone = false;
    const flushRender = ()=>{
      renderQueued = false;
      if(!cur || streamDone) return;
      cur.dataset.raw = curText;
      aiFill(cur, curText, {hl:false});
      $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
    };
    while(true){
      const {done, value} = await reader.read();
      if(done) break;
      buf += dec.decode(value, {stream:true});
      let i;
      while((i = buf.indexOf('\n\n')) >= 0){
        const line = buf.slice(0, i).trim(); buf = buf.slice(i+2);
        if(!line.startsWith('data: ')) continue;
        let ev; try{ ev = JSON.parse(line.slice(6)); }catch(_){ continue; }
        aiTraceEvent(ev);   // 旁路采集：一个事件 → 轨迹里一个节点（不改动下面任何渲染逻辑）
        if(ev.type === 'delta'){
          dropThink();
          if(!cur){ cur = aiAdd('ai', '', aiNow(), reaskText); curTs = cur.dataset.ts || ''; curText = ''; }
          curText += ev.text;
          if(!renderQueued){ renderQueued = true; requestAnimationFrame(flushRender); }
        } else if(ev.type === 'reasoning'){
          // 思考型模型的 reasoning_content 流：在"思考中"气泡里滚动展示尾部
          if(ev.text){
            rsnText += ev.text;
            // 气泡可能已被 plan_start 删掉（它是服务端第一个事件，比
            // reasoning 先到）——重建它，否则尾部展示永远是死代码。
            mkThink();
            // parentNode 检查：气泡删了再建之后，rsnEl 还指向已脱离 DOM 的
            // 旧节点，只判 !rsnEl 会把文字写进一个看不见的元素里。
            if(!rsnEl || rsnEl.parentNode !== think){
              rsnEl = document.createElement('div'); rsnEl.className = 'ai-think-txt'; think.appendChild(rsnEl);
            }
            /* 与正文同一个 rAF 节流：思考流一次回合能来上千条，逐条
               textContent + scrollTop 是强制同步布局，网络 pace 会直接打到
               layout 上（比正文流更糟，正文好歹合并过）。 */
            if(!rsnQueued){ rsnQueued = true; requestAnimationFrame(()=>{
              rsnQueued = false;
              if(!rsnEl || rsnEl.parentNode !== think) return; // 气泡被删：放弃这次写入
              rsnEl.textContent = '…' + rsnText.slice(-140);
              $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
            }); }
          }
        } else if(ev.type === 'plan_start'){
          // v2.3 N5：开跑前告诉用户这一轮的上限与理由。
          // 以前是静默开始、跑到 8 轮静默停下，用户两头都不知道。
          dropThink();
          planBox(ev.max_turns, ev.reason);
        } else if(ev.type === 'plan_step'){
          // 每步都显示"正在做什么"——用户看着 AI 查了什么，
          // 比只看结论更可信（结论要能追到查过的东西）。
          planStep(ev.seq, ev.text);
        } else if(ev.type === 'plan_end'){
          planEnd(ev.text);
        } else if(ev.type === 'plan_extend'){
          planExtend(ev.max_turns, ev.reason);
        } else if(ev.type === 'tool_result'){
          dropThink();
          petAIState('busy'); // 桌宠联动：工具真正在执行了。原来这行挂在死的 tool_call 分支上（后端从不发那个事件），等于从没生效过
          const d = document.createElement('details'); d.className = 'ai-tool';
          /* 图标跟服务端 ok 走：失败还顶着绿勾是最糟的一种误导
             （用户扫一眼觉得"工具都成功了"，实际全是红字）。 */
          const tokOk = (typeof ev.ok === 'boolean') ? ev.ok : true;
          const sm = document.createElement('summary');
          sm.textContent = (tokOk ? '\u2713 ' : '\u2717 ') + aiToolName(ev.tool) + ' \u00b7 ' + t('查看结果');
          const pre = document.createElement('pre'); pre.textContent = ev.result || '';
          d.appendChild(sm); d.appendChild(pre);
          $ai('ai-msgs').appendChild(d);
          $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
        } else if(ev.type === 'verify_result'){
          // v2.9：客观复验结果直接上界面。
          // **不用模型的话描述成败** —— 徽章只认 SSE 里的 ok/verified。
          // 三态必须可辨：ok=false=未通过；ok=true&verified=false=已执行但没复验；
          // 两者合并成一种颜色就等于把这个功能白做。
          dropThink();
          aiRenderVerify(ev);
        } else if(ev.type === 'confirm_request'){
          dropThink();
          // 助手 tool_calls 消息进历史（协议要求 tool 结果必须跟随）
          const asst = {role:'assistant', content: curText, tool_calls:[{id:ev.tool_call_id, type:'function', function:{name:ev.tool, arguments:JSON.stringify(ev.args||{})}}]};
          if(rsnText) asst.reasoning_content = rsnText;
          if(curTs) asst.ts = curTs;
          aiMessages.push(asst);
          // v3.2：服务端在**同一个 SSE 流里**等我们的决定，这一轮还没结束。
          // 把当前气泡收尾：确认之后模型接着说的一段要另起一条，不能拼到旧气泡上
          // （拼上去会出现"结论 + 执行结果"粘在同一段里的错位）。
          cur = null; curText = ''; rsnText = ''; curTs = '';
          // 确认卡片用 DOM + textContent 构建：ev.text 含模型给的 args，直接 innerHTML 是注入面
          const card = document.createElement('div');
          card.className = 'ai-confirm';
          const cb = document.createElement('b'); cb.textContent = t('AI 请求执行：'); card.appendChild(cb);
          card.appendChild(document.createTextNode(ev.text || ev.tool));
          card.appendChild(document.createElement('br'));
          card.appendChild(document.createTextNode(t('确认后将真实执行该操作。')));
          // 批量授权（v3.2）：只有服务启停族给这个勾选。
          // 为什么只给这三个：它们在同一次任务里天然连着（停 → 起 → 复验），
          // 逐次点头换来的是三次上下文重建而不是安全；数据/系统类动作不在此列。
          let batchBox = null;
          if(AI_BATCH_TOOLS[ev.tool]){
            const lab = document.createElement('label'); lab.className = 'ai-batch';
            batchBox = document.createElement('input'); batchBox.type = 'checkbox';
            const sp = document.createElement('span');
            sp.textContent = t('本任务内后续同类操作不再询问（最多 2 次，仅限服务启停）');
            lab.appendChild(batchBox); lab.appendChild(sp); card.appendChild(lab);
          }
          const btns = document.createElement('div'); btns.className = 'btns';
          const by = document.createElement('button'); by.className = 'yes'; by.textContent = t('确认执行');
          const bn = document.createElement('button'); bn.className = 'no'; bn.textContent = t('拒绝');
          btns.appendChild(by); btns.appendChild(bn); card.appendChild(btns);
          $ai('ai-msgs').appendChild(card);
          $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
          // 键盘可达：卡弹出后把焦点放到"确认执行"，Tab 两次就能批/拒。
          // 不移焦点的话，键盘用户要从输入框 Tab 过十几步才能摸到这两个按钮。
          try{ by.focus(); }catch(_){}
          aiPending = {asst, card, id: ev.tool_call_id, tool: ev.tool, args: ev.args || {},
            batchOf: ()=> !!(batchBox && batchBox.checked)};
          aiHstat(t('等你确认'));
          // 桌宠联动：等确认状态 + 蓝色徽标，点徽标/点宠物直接在迷你卡里批，不必开抽屉
          petAskConfirm(t('AI 请求执行'), ev.text || ev.tool, ()=>aiDecide(true), ()=>aiDecide(false));
          by.onclick = ()=>aiDecide(true);
          bn.onclick = ()=>aiDecide(false);
        } else if(ev.type === 'usage'){
          // 本轮消耗：尾部一行摘要（点开是 DSH 式明细卡），并累计到桌宠"今日统计"
          if(ev.usage){
            aiUsageAdd(ev.usage);
            aiTurnUsage = Object.assign({}, ev.usage, {
              model: ev.model || '', provider: ev.provider || '',
              steps: aiTurnNodes.length, ms: aiTurnStartAt ? (Date.now() - aiTurnStartAt) : 0
            });
            const host = cur || aiTurnAnchor;
            if(host) aiUsageLine(host, aiTurnUsage);
            if(aiBigOn && aiSideTab === 'usage') aiUsageRender();
            $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
          }
        } else if(ev.type === 'waiting_confirm'){
          // 服务端已在本请求里等我们点按（v3.2）：这一轮没有结束，别当它已经答完
          aiHstat(t('等你确认'));
        } else if(ev.type === 'body_recovered'){
          // 服务端没收到完整请求体（代理/容器吃掉了），用请求头里的最近几条兜底恢复。
          // 必须让用户看见：他有权知道"刚才那段上下文没送达"，否则只会觉得模型变笨了。
          aiBodyWarn(ev.messages);
          aiTraceAdd('body', t('请求体未到达，已恢复 {0} 条消息', ev.messages), {cls:'warn'});
        } else if(ev.type === 'grant_open'){
          // 用户勾了"后续同类不再问"：把这件事显式说出来。
          // 不显示的话，后面几步不再弹卡就会变成"它怎么自己动了"——授权必须可见。
          aiTraceAdd('grant', t('已开启批量授权：{0} · 本任务内最多 {1} 次', t(ev.label || ''), ev.left), {cls:'warn'});
          aiAdd('sys', t('已授权：本任务内{0}最多再执行 {1} 次，不再逐次询问', t(ev.label || ''), ev.left));
          aiHstat(t('批量授权 · 剩 {0} 次', ev.left));
        } else if(ev.type === 'grant_used'){
          aiTraceAdd('grant', t('批量授权执行：{0}（剩余 {1} 次）', ev.text || aiToolName(ev.tool), ev.left), {cls:''});
          aiAdd('sys', t('已用批量授权执行：{0}（剩余 {1} 次）', ev.text || aiToolName(ev.tool), ev.left));
          aiHstat(t('批量授权 · 剩 {0} 次', ev.left));
        } else if(ev.type === 'info'){
          // 服务端阶段性提示（如"正在压缩较早的对话历史"）：换掉思考气泡的文案。
          // 不发这种提示时，摘要那次前置调用最长 60s 里用户只看到三点动画，
          // 第一反应就是"卡死了"去刷新，白烧一次 token。
          dropThink(); mkThink();
          const tip = document.createElement('div'); tip.className = 'ai-think-txt';
          // 后端发的是中文原文；t() 查不到键时原样返回，不会更糟
          tip.textContent = t(ev.text || '');
          think.appendChild(tip);
          $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
        } else if(ev.type === 'error'){
          dropThink(); aiErr(ev.text);
        }      }
    }
    if(cur){
      streamDone = true;             // 先关掉节流回调，免得它在最终渲染后把高亮冲掉
      cur.dataset.raw = curText;
      aiFill(cur, curText);          // 这一帧带代码高亮
      const fin = {role:'assistant', content: curText}; if(rsnText) fin.reasoning_content = rsnText; if(curTs) fin.ts = curTs; aiMessages.push(fin);
    }
  }catch(e){
    if(gen !== aiGen) return; // 已有新流开跑：这次 abort 是被取代的旧流，别再贴"已停止"消息
    if(e && e.name === 'AbortError'){ aiAdd('sys', t('已停止生成')); }
    else { if(think && think.parentNode) think.remove(); aiErr(t('连接中断：') + e); }
  }
  finally{
    // 只有**当代际**才复位全局态：旧流晚到的 finally 不能把新流的
    // aiBusy/按钮态冲掉（那会让输入框在流式中途重新可用，两条流交错贴消息）。
    if(gen === aiGen){
      if(think && think.parentNode) think.remove();
      aiBusy = false; aiCtrl = null;
    // 轨迹与用量**挂到消息元素上**：工具栏的「轨迹/导出」按元素取，
    // 不依赖"当前指向哪一轮"的全局指针——用户回头看历史回合时才不会串。
    const _dur = aiTurnStartAt ? (Date.now() - aiTurnStartAt) : 0;
    const _nodes = aiTurnNodes.slice();
    [aiTurnAnchor, cur].forEach(el=>{
      if(!el) return;
      el.__trace = _nodes; el.__turnUsage = aiTurnUsage; el.__dur = _dur;
    });
    const _u = aiTurnUsage || {};
    const _pct = aiCachePct(_u);
    aiTurnLog.push({at: aiNow().slice(6), tin: Number(_u.prompt_tokens) || 0, tout: Number(_u.completion_tokens) || 0,
      hit: Number(_u.cache_hit) || 0, miss: Number(_u.cache_miss) || 0, steps: _nodes.length, ms: _dur,
      model: _u.model || ''});
    if(aiTurnUsage){
      aiHstat('↑' + aiFmtTok(_u.prompt_tokens) + ' ↓' + aiFmtTok(_u.completion_tokens) +
        (_pct !== null ? ' · ' + t('缓存 {0}%', Math.round(_pct * 100)) : '') + ' · ' + t('{0} 步', _nodes.length));
    }
    if(aiBigOn && aiSideTab === 'usage') aiUsageRender();
    $ai('ai-send').disabled = false; $ai('ai-stop').style.display = 'none';
    aiHistSave();   // 回合结束落盘（含确认卡决定后的回合）
    // 回合结束：思考/干活状态回待机；等确认或出错的状态保留（由用户决定/看日志后复位）
    petAIIdle();
    } // if(gen === aiGen)
  }
}
/* 手动停止：中断上游流式请求（服务端检测到断开会同步中止） */
$ai('ai-stop').onclick = ()=>{ if(aiCtrl){ try{ aiCtrl.abort(); }catch(e){} } };

$ai('ai-send').onclick = async ()=>{
  if(aiBusy) return;
  const raw = $ai('ai-in').value.trim();
  if(!raw) return;
  // 已选文件只以"路径引用"的形式并入这条消息（读取由 read_file 在沙箱内完成）
  const tx = aiFileRef() + raw;
  $ai('ai-in').value = '';
  // 复用 aiSendText（剥悬挂卡、记时间戳、进历史、起流式回合都在里面）——
  // 这里原先把那套动作又抄了一遍，改一处漏一处的事故已经发生过一次。
  await aiSendText(tx);
};
$ai('ai-in').addEventListener('keydown', e=>{
  // isComposing：中文输入法里按 Enter 是确认候选词，不是发送（否则拼音还没上屏消息就飞出去了）
  if(e.isComposing || e.keyCode === 229) return;
  if(e.key === 'Enter' && !e.shiftKey){ e.preventDefault(); $ai('ai-send').click(); }
});

/* ---------- 错误一键发 AI 分析（桌宠右键「分析最近错误」） ---------- */
function collectErrLines(){
  const out = [];
  for(const k in LOG){
    const L = LOG[k]; if(!L || !L.lines) continue;
    L.lines.forEach(l=>{ if(lvlOf(l)==='err' || /\[(FAIL|ERROR)\]|(失败|错误)/.test(l)) out.push('[' + k + '] ' + l); });
  }
  return out.slice(-15);
}
async function aiAnalyzeError(){
  const lines = collectErrLines();
  if(!lines.length){ toast(t('最近的日志里没有发现错误')); return; }
  if(aiBusy){ toast(t('助手正在忙，稍后再试'), 'err'); return; }
  aiOpen();
  const tx = t('我遇到了环境问题，以下是最近的错误日志。请帮我定位原因并给出解决步骤：\n') + lines.join('\n');
  await aiSendText(tx);
}

/* 诊断报告 AI 解读 */
$ai('btn-ai-explain').onclick = ()=>{
  if(aiBusy) return;   // 连点两次会并发两条流（aiConsume 入口原本不设防）
  aiMessages = [];
  aiConsume('/api/ai/explain', {});
};
$ai('btn-ai-open-chat').onclick = aiOpen;


/* v3.3：日志默认折叠与审计趋势折叠已改为标记层初始态（class=collapsed / <details>），
   这里原先那段'加载后替用户点按钮'的脚本已删除——界面初始状态不该依赖脚本时序。 */

/* ==================== 设置页（v3.4） ====================
   三区共用同一套只读行渲染（ekRo）：基线与看板本质都是「键 → 值 / 出处」。
   两条铁律：
   1) 打开就重读（goto 里调 ekSettingsEnter）：本页与「程序配置」「链端校验」
      共用同一份 config.json，任何一侧改完，另一侧不该还显示旧值。
   2) 缺数据不许显示成 0 或「—」，那等于谎报 —— 一律写清缺什么。
*/
/* EK_SET 已声明在文件顶部（goto 的深链调用早于本段求值，见那里的注释）。 */

function ekSettingsEnter(){ ekSetPane(EK_SET.tab || 'base'); }

function ekSetPane(name){
  const tabs = document.getElementById('set-tabs');
  if(tabs) tabs.querySelectorAll('.cfg-tab').forEach(b=>b.classList.toggle('on', b.dataset.setTab===name));
  document.querySelectorAll('#panel-settings .cfg-pane').forEach(p=>p.classList.toggle('show', p.dataset.setPane===name));
  EK_SET.tab = name;
  if(name==='base') ekSetLoadBase();
  else if(name==='dev') ekSetLoadDev();
  else if(name==='eval'){ ekEvLoad(); ekEvPoll(true); }
  if(name!=='eval') ekEvPoll(false);
}

/* 只读键值列表：rows = [{k, v, cls}]，cls 取 ok/bad/warn */
function ekRo(sel, rows){
  const el = typeof sel==='string' ? $(sel) : sel;
  if(!el) return;
  if(!rows || !rows.length){ el.innerHTML = '<div class="ro-empty">' + t('暂无可显示的内容') + '</div>'; return; }
  el.innerHTML = rows.map(r=>{
    const v = (r.v==null || r.v==='') ? '—' : r.v;
    /* 键也过一遍 t()：这些标签是渲染时拼进 innerHTML 的，applyI18n 扫不到，
       不过 t() 的话英文界面下这里会是一列中文，和旁边已经译好的文字混排。 */
    return '<div class="ro-row"><span class="k">'+esc(t(r.k))+'</span>' +
           '<span class="v'+(r.cls?' '+r.cls:'')+'">'+esc(v)+'</span></div>';
  }).join('');
}

/* ---------- 基础 ---------- */

async function ekSetLoadBase(){
  const st = $('#h-set-save');
  if(st) st.textContent = t('读取中…');
  try{
    const [c, pet, ai] = await Promise.all([
      fetch('/api/config').then(r=>r.json()),
      fetch('/api/pet/config').then(r=>r.json()).catch(()=>null),
      fetch('/api/ai/config').then(r=>r.json()).catch(()=>null)
    ]);
    EK_SET.base = c;
    cfg = c;                       // 与全局快照对齐：老页面的 configBody() 才有完整底稿
    const p = c.projects || {}, px = c.proxy || {}, ch = c.chain || {};
    $('#s_install_dir').value   = c.install_dir || '';
    $('#s_proxy_http').value    = px.http || '';
    $('#s_proxy_https').value   = px.https || '';
    $('#s_goproxy').value       = c.goproxy || '';
    $('#s_gosumdb').value       = c.gosumdb || '';
    $('#s_go_cgo').value        = c.go_cgo || '';
    $('#s_npm_registry').value  = c.npm_registry || '';
    $('#s_fe_dir').value        = p.frontend_dir || '';
    $('#s_be_dir').value        = p.backend_dir || '';
    $('#s_app_name').value      = c.app_name || '';
    $('#s_app_title').value     = c.app_title || '';
    $('#s_update_url').value    = c.update_url || '';

    /* 数据库/链端只读：密码只回「是否设置」，不在页面上再放一个可被读屏念出来的输入框 */
    ekRo('#s_db_view', [
      {k:'连接', v:(p.mysql_host||'127.0.0.1')+':'+(p.mysql_port||3306)},
      {k:'用户', v:p.mysql_user||''},
      {k:'数据库名', v:p.db_name||''},
      {k:'密码', v:p.mysql_password ? t('已设置（DPAPI 加密存储）') : t('未设置'), cls:p.mysql_password?'ok':'warn'}
    ]);
    ekRo('#s_chain_view', [
      {k:'链节点', v:(ch.chain_host||'')+(ch.chain_port?':'+ch.chain_port:''), cls:ch.chain_host?'':'bad'},
      {k:'登录', v:(ch.ssh_user||'')+(ch.ssh_host?('@'+ch.ssh_host):'')+(ch.ssh_port?(':'+ch.ssh_port):'')},
      {k:'节点目录', v:ch.chain_dir||''},
      {k:'WeBASE', v:(ch.webase_dir||'')+(ch.webase_port?(' · :'+ch.webase_port):'')},
      {k:'群组', v:ch.group_id||1},
      {k:'后台守护', v:ch.chain_guard ? t('已开启（{0} 秒/次）', ch.chain_guard_secs||60) : t('关闭'), cls:ch.chain_guard?'ok':'warn'}
    ]);

    if(pet){
      $('#s_weather_on').checked = !!pet.weather_enabled;
      $('#s_weather_city').value = pet.weather_city || '';
    }
    if(ai){
      ekRo('#s_ai_view', [
        {k:'AI 助手', v: ai.key_set ? (ai.enabled ? t('已启用') : t('已配密钥但未启用')) : t('未配置密钥'),
         cls: (ai.key_set && ai.enabled) ? 'ok' : 'warn'},
        {k:'模型', v: ai.model || ''},
        {k:'记忆', v: ai.memory_enabled ? t('已开启') : t('关闭')}
      ]);
    }
    if(st) st.textContent = t('已读取最新配置（{0}）', new Date().toLocaleTimeString());
    /* 长 URL（GOPROXY / npm 源）在窄卡片里会被截断：把完整值放进 title，
       悬停就能核对 —— 比为了看清而把卡片拉成整行更划算。 */
    ['#s_goproxy','#s_gosumdb','#s_npm_registry','#s_proxy_http','#s_proxy_https','#s_install_dir','#s_fe_dir','#s_be_dir'].forEach(sel=>{
      const el = $(sel);
      if(el) el.title = el.value || '';
    });
  }catch(e){
    if(st) st.textContent = t('读取失败：{0}', e);
  }
}

/* 设置页的目录选择：不复用老页面的 pickDir —— 那个函数会顺带写 #fe_dir 并触发
   markDirty()，等于在设置页点一下「选择」就把老页表单存一遍，两边互相覆盖。 */
function ekSetPick(btn){
  const id = btn.dataset.spick;
  const cur = (document.getElementById(id)||{}).value || '';
  openBrowser('dir', cur, '', 'dir', p=>{ if(p){ document.getElementById(id).value = p; } });
}

async function ekSetSaveBase(){
  const st = $('#h-set-save');
  if(!EK_SET.base){ toast(t('还没读到配置，请先点「重新读取」'), 'err'); return; }
  const c = JSON.parse(JSON.stringify(EK_SET.base));   // 深拷贝：失败时不留半改的底稿
  c.install_dir = $('#s_install_dir').value.trim();
  c.proxy = {http:$('#s_proxy_http').value.trim(), https:$('#s_proxy_https').value.trim()};
  c.goproxy = $('#s_goproxy').value.trim();
  c.gosumdb = $('#s_gosumdb').value.trim();
  c.go_cgo  = $('#s_go_cgo').value.trim();
  c.npm_registry = $('#s_npm_registry').value.trim();
  c.projects = c.projects || {};
  c.projects.frontend_dir = $('#s_fe_dir').value.trim();
  c.projects.backend_dir  = $('#s_be_dir').value.trim();
  c.app_name  = $('#s_app_name').value.trim();
  c.app_title = $('#s_app_title').value.trim();
  c.update_url = $('#s_update_url').value.trim();

  if(st) st.textContent = t('保存中…');
  try{
    const r = await fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(c)});
    if(r.ok){
      if(st) st.textContent = t('已保存（{0}）', new Date().toLocaleTimeString());
      toast(t('基础设置已保存'));
      /* 老页面 DOM 里还是旧值：不重填的话，用户在「程序配置」随手改一个输入框，
         那次自动保存就会把刚设的目录/代理按旧值写回去。必须重读一次。 */
      try{ await loadConfig(); }catch(e){}
      await ekSetLoadBase();
    }else{
      let msg = 'HTTP ' + r.status;
      try{
        const j = await r.json();
        if(j.problems && j.problems.length) msg = j.problems.map(x=>(x.field||'')+' → '+(x.msg||'')).join('；');
        else if(j.error) msg = j.error;
      }catch(e){}
      if(st) st.textContent = t('保存失败：{0}', msg);
      toast(t('保存失败：')+msg, 'err');
    }
  }catch(e){
    if(st) st.textContent = t('保存失败：{0}', e);
  }
}

/* 天气是独立出入口：/api/pet/config 用 *bool，绝不能混进 /api/config 的整包保存 */
async function ekSetWeather(){
  const on = $('#s_weather_on').checked;
  const city = $('#s_weather_city').value.trim();
  try{
    const r = await fetch('/api/pet/config',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({weather_enabled:on, weather_city:city})});
    if(r.ok){ toast(on ? t('天气已开启（{0}）', city||t('默认城市')) : t('天气已关闭')); }
    else toast(t('保存失败：HTTP {0}', r.status), 'err');
  }catch(e){ toast(t('网络错误：')+e, 'err'); }
}

/* ---------- 开发者 ----------
   11 个字段只有一个保存口（/api/dev/config），全部是 int 指针：填 0 或留空 = 回落内置默认。
   默认值只在后端 state.go 里有一份（devDefault*），前端不复制字面量 ——
   「恢复默认」的语义就是"把 dev 段清成 0"，而不是在前端写第二份数字。 */
const EK_DEV_MAP = [
  {k:'summary_after_tokens',  s:'#sd_summary_after',  n:'#sd_summary_after_n'},
  {k:'summary_keep_tokens',   s:'#sd_summary_keep',   n:'#sd_summary_keep_n'},
  {k:'summarize_keep_msgs',   s:'#sd_keep_msgs',      n:'#sd_keep_msgs_n'},
  {k:'turn_timeout_secs',     s:'#sd_turn_timeout',   n:'#sd_turn_timeout_n'},
  {k:'audit_retention_days',  s:'#sd_audit_days',     n:'#sd_audit_days_n'},
  {k:'trace_retention_days',  s:'#sd_trace_days',     n:'#sd_trace_days_n'},
  {k:'shadow_retention_days', s:'#sd_shadow_days',    n:'#sd_shadow_days_n'},
  {k:'mission_deadline_days', s:'#sd_mission_dl',     n:'#sd_mission_dl_n'},
  {k:'mission_keep_days',     s:'#sd_mission_keep',   n:'#sd_mission_keep_n'},
  {k:'server_port',           s:null,                 n:'#sd_server_port'},
  {k:'port_scan_limit',       s:null,                 n:'#sd_port_limit'}
];

function ekDevBindSliders(){
  EK_DEV_MAP.forEach(m=>{
    if(!m.s) return;
    const s = $(m.s), n = $(m.n);
    if(!s || !n) return;
    s.addEventListener('input', ()=>{ n.value = s.value; });
    /* 数字→滑块只做视觉映射，不回写数字框：用户输入 0（= 恢复默认）时
       若把 0 夹到滑块 min 再写回去，就永远填不出 0 了。 */
    n.addEventListener('input', ()=>{
      const v = parseInt(n.value, 10);
      if(isNaN(v)) return;
      const lo = parseInt(s.min,10) || 0, hi = parseInt(s.max,10) || 1e9;
      s.value = Math.max(lo, Math.min(hi, v));
    });
  });
}

async function ekSetLoadDev(){
  const st = $('#h-sd-save');
  if(st) st.textContent = t('读取中…');
  try{
    /* 开发者区的「恢复预算」只读块来自 config.json 的 recovery 段：
       直接点「开发者」tab 时基础区还没跑过，这里补一次，否则那块永远是空的。 */
    if(!EK_SET.base) await ekSetLoadBase();
    const d = await fetch('/api/dev/config').then(r=>r.json());
    /* 怪癖是自动学习出来的运行态，不在 dev 段里 —— 单独取一次，失败也不影响其它块 */
    fetch('/api/ai/config').then(r=>r.json()).then(j=>{
      EK_SET.aiQuirk = j.quirk || {};
      ekSetRenderQuirk();
    }).catch(()=>{ EK_SET.aiQuirk = {}; ekSetRenderQuirk(); });
    /* 仓库根只在 eval status 里（它要真去 stat tools/eval_gate.py）；
       没进过评测 tab 时这里补一次，否则运行时块永远显示"未定位"。 */
    if(!EK_SET.ev){
      fetch('/api/eval/status').then(r=>r.json()).then(j=>{ EK_SET.ev = j; ekSetRenderRuntime(); }).catch(()=>{});
    }
    EK_SET.dev = d;
    EK_DEV_MAP.forEach(m=>{
      const v = d[m.k];
      const n = $(m.n);
      if(n) n.value = (v==null ? '' : v);
      const s = $(m.s);
      if(s && v!=null){
        const lo = parseInt(s.min,10)||0, hi = parseInt(s.max,10)||1e9;
        s.value = Math.max(lo, Math.min(hi, v));
      }
    });
    /* 端口状态：拿浏览器实际访问的端口和 dev 段起始端口对照 ——
       两者不一致说明当前实例是顺延出来的，改起始端口要重启才见效。 */
    const live = parseInt(location.port || '80', 10);
    const want = parseInt(d.server_port, 10) || 18765;
    const el = $('#st-dev-port');
    if(el){
      el.textContent = live===want ? t('当前 :{0}', live) : t('当前 :{0}（设定 {1}）', live, want);
      el.className = 'status ' + (live===want ? 'ok' : 'warn');
    }
    const hp = $('#h-dev-port');
    if(hp) hp.textContent = live===want
      ? t('改端口需要重启 EnvKit 才生效；评测脚本硬编码 18765，改端口后后两档会被判「不可用」。')
      : t('当前实例不在 {0} 上：要么该端口被占用后顺延了，要么刚改过还没重启。评测脚本硬编码 18765，端口不一致时后两档会拒绝启动。', want);

    ekSetRenderRuntime();
    ekSetRenderQuirk();
    ekSetRenderRecovery();
    if(st) st.textContent = t('已读取（{0}）', new Date().toLocaleTimeString());
  }catch(e){
    if(st) st.textContent = t('读取失败：{0}', e);
  }
}

/* 运行时只读块：只放"事实"，不放能改的开关（能改的都在上面的组里） */
function ekSetRenderRuntime(){
  const verEl = document.getElementById('about-ver');
  const ver = verEl ? verEl.textContent.replace(/^.*v/, '') : '';
  const rows = [
    {k:'版本', v: ver || t('未知')},
    {k:'控制台端口', v: location.port || '(80)'},
    {k:'安装目录', v: (EK_SET.base && EK_SET.base.install_dir) || ''},
    {k:'仓库根', v: (EK_SET.ev && EK_SET.ev.root) || t('未定位（评测脚本只在仓库目录下）'),
     cls: (EK_SET.ev && EK_SET.ev.root) ? 'ok' : 'warn'}
  ];
  ekRo('#sd_runtime', rows);
}

function ekSetRenderQuirk(){
  const q = EK_SET.aiQuirk;
  if(!q){ ekRo('#sd_quirk', [{k:'状态', v:t('读取中…')}]); return; }
  const rows = [
    {k:'温度参数', v: q.temp_mode==='one' ? t('固定发 1（该模型只接受 1）')
                : (q.temp_mode==='none' ? t('不发送温度') : t('按配置值发送'))},
    {k:'max_tokens 字段', v: q.max_tokens_field==='max_completion_tokens' ? 'max_completion_tokens' : 'max_tokens'},
    {k:'工具调用', v: q.no_tools ? t('不支持（已自动降级）') : t('支持'), cls: q.no_tools?'warn':'ok'}
  ];
  ekRo('#sd_quirk', rows);
}

function ekSetRenderRecovery(){
  const r = (EK_SET.base && EK_SET.base.recovery) || {};
  const rows = [
    {k:'窗口内动作上限', v: r.max_actions || t('内置默认（12）')},
    {k:'预算窗口',      v: r.window_secs ? t('{0} 秒', r.window_secs) : t('内置默认（600 秒）')},
    {k:'单目标重试上限', v: r.max_attempts || t('内置默认（3）')},
    {k:'断路器阈值',    v: r.breaker_after || t('内置默认（3）')},
    {k:'熔断冷静期',    v: r.breaker_cooldown_secs ? t('{0} 秒', r.breaker_cooldown_secs) : t('内置默认（300 秒）')}
  ];
  ekRo('#sd_recovery', rows);
}

async function ekSetSaveDev(){
  const st = $('#h-sd-save');
  const body = {};
  EK_DEV_MAP.forEach(m=>{
    const n = $(m.n);
    if(!n) return;
    const v = parseInt(n.value, 10);
    body[m.k] = isNaN(v) ? 0 : v;      // 空 = 0 = 恢复默认，这是显式语义不是"没填"
  });
  if(st) st.textContent = t('保存中…');
  try{
    const r = await fetch('/api/dev/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    let j = {}; try{ j = await r.json(); }catch(e){}
    if(r.ok){
      if(st) st.textContent = t('已保存（{0}）', new Date().toLocaleTimeString());
      toast(t('开发者配置已保存；端口改动需重启生效'));
      await ekSetLoadDev();
    }else{
      const msg = (j.problems && j.problems.length)
        ? j.problems.map(x=>(x.field||'')+' → '+(x.msg||'')).join('；')
        : (j.error || ('HTTP ' + r.status));
      if(st) st.textContent = t('保存失败：{0}', msg);
      toast(t('保存失败：')+msg, 'err');
    }
  }catch(e){
    if(st) st.textContent = t('保存失败：{0}', e);
  }
}

/* 恢复默认 = 把 11 个字段全部清 0（后端 devOr 会把 0 当"没设"回落内置默认）。
   滑块同步到 min 只是视觉近似：0 不在量程内，滑到最左即可，数字框保留真实的 0。 */
function ekSetResetDev(){
  EK_DEV_MAP.forEach(m=>{
    const n = $(m.n);
    if(n) n.value = 0;
    const s = $(m.s);
    if(s) s.value = s.min;
  });
  const st = $('#h-sd-save');
  if(st) st.textContent = t('已清空为默认（点「保存」生效）');
}

/* ---------- 评测 ----------
   四档全部走 POST /api/eval/run：
     smoke / gate  只读，点了就跑；
     recovery / tasks 破坏性，服务端强制 428，前端只能弹确认卡后带 confirm 重发。
   前端不做任何"乐观启动"：按钮状态一律以 GET /api/eval/status 的 running 为准，
   否则点了没反应和点了跑了分不清。 */

function ekPct(v){
  if(v==null || isNaN(Number(v))) return '';
  return (Math.round(Number(v)*1000)/10) + '%';
}

async function ekEvLoad(){
  try{
    const s = await fetch('/api/eval/status').then(r=>r.json());
    EK_SET.ev = s;
    ekEvRenderEnv(s);
    ekEvRenderKinds(s);
    ekEvRenderBaseline(s);
    ekEvRenderBoards(s);
    ekEvRenderReports(s);
    ekSetRenderRuntime();
    return s;
  }catch(e){
    const st = $('#st-eval-env');
    if(st){ st.textContent = t('检测失败'); st.className = 'status err'; }
    ekRo('#ev-env', [{k:'错误', v:String(e), cls:'bad'}]);
    return null;
  }
}

function ekEvRenderEnv(s){
  const st = $('#st-eval-env');
  const py = s.python || {}, port = s.port || {}, ai = s.ai || {};
  if(st){
    const ok = py.ok && !s.root_error;
    st.textContent = ok ? t('可用') : t('部分不可用');
    st.className = 'status ' + (ok ? 'ok' : 'warn');
  }
  const rows = [
    {k:'仓库根', v: s.root || s.root_error || '', cls: s.root ? 'ok' : 'bad'},
    {k:'Python', v: py.ok ? (py.path + (py.version ? ' · ' + py.version : '')) : (py.why || t('未找到')), cls: py.ok?'ok':'bad'},
    {k:'控制台端口', v: port.ok ? t('{0}（与脚本一致）', port.listen) : (port.note || t('{0}', port.listen)), cls: port.ok?'ok':'warn'},
    {k:'AI', v: ai.key_set ? t('已配置（{0}）', ai.model || t('默认模型')) : t('未配置密钥：后两档不可用'), cls: ai.key_set?'ok':'warn'}
  ];
  const last = s.last;
  if(last){
    const t0 = last.finished_at || '';
    rows.push({k:'上次评测', v: t('{0} · {1} · {2} 秒 · {3} 行', last.name||last.kind, last.ok?t('通过'):t('未通过'), Math.round(last.seconds||0), last.lines||0),
               cls: last.ok?'ok':'bad'});
    if(t0) rows.push({k:'完成时间', v:t0});
    if(last.summary) rows.push({k:'结论', v:last.summary});
    if(last.gate && last.gate.problems && last.gate.problems.length){
      rows.push({k:'门禁问题', v:last.gate.problems.join('；'), cls:'bad'});
    }
  }
  if(s.running){
    rows.push({k:'正在运行', v:t('{0} · 已 {1} 秒{2}', s.running.name, s.running.elapsed_secs, s.running.dry_run?t('（预演）'):''), cls:'warn'});
  }
  ekRo('#ev-env', rows);
  const h = $('#h-ev-env');
  if(h) h.textContent = '';
}

/* 轮询重渲染会重建卡片，输入框里的 --only / 重复次数必须先存后填，否则每 2.5 秒被清一次 */
function ekEvSnapshotInputs(){
  const o = {};
  document.querySelectorAll('#ev-kinds [data-ev-in]').forEach(el=>{
    o[el.id] = (el.type === 'checkbox') ? el.checked : el.value;
  });
  return o;
}
function ekEvRestoreInputs(o){
  Object.keys(o||{}).forEach(id=>{
    const el = document.getElementById(id); if(!el) return;
    if(el.type === 'checkbox') el.checked = !!o[id]; else el.value = o[id];
  });
}

function ekEvRenderKinds(s){
  const box = $('#ev-kinds'); if(!box) return;
  const keep = ekEvSnapshotInputs();
  const running = s.running || null;
  box.innerHTML = (s.kinds||[]).map(k=>{
    const cls = ['ev-card'];
    if(!k.read_only) cls.push('danger');
    if(!k.available) cls.push('na');
    const badge = k.read_only
      ? '<span class="ev-badge ro">' + t('只读') + '</span>'
      : '<span class="ev-badge rw">' + t('破坏性') + '</span>';
    let ops = '';
    if(k.key === 'recovery'){
      ops = '<input type="text" data-ev-in id="ev_only_' + k.key + '" placeholder="--only 如 kill_service" title="只跑指定注入类，逗号分隔" />' +
            '<label class="ev-chk" title="自动批准确认卡（白名单只有 start_service）；不开的话样本会被确认卡掐断，覆盖率必然偏低">' +
            '<input type="checkbox" data-ev-in id="ev_aw_' + k.key + '">' + t('允许写操作') + '</label>' +
            '<button class="ev-run ghost" data-ev="dry" data-kind="' + k.key + '">' + t('预演') + '</button>';
    }else if(k.key === 'tasks'){
      ops = '<input type="text" data-ev-in id="ev_only_' + k.key + '" placeholder="--only 如 T01,T02" title="只跑指定题目，逗号分隔" />' +
            '<input type="number" class="set-num" data-ev-in id="ev_repeat_' + k.key + '" min="0" max="10" placeholder="重复" title="重复次数；0 = 用脚本默认" />' +
            '<button class="ev-run ghost" data-ev="dry" data-kind="' + k.key + '">' + t('预演') + '</button>';
    }
    const isRun = running && running.kind === k.key;
    const btn = isRun
      ? '<button class="ev-run warn" data-ev="stop">' + t('停止') + '</button>'
      : '<button class="ev-run" data-ev="run" data-kind="' + k.key + '"' + (k.available ? '' : ' disabled') + '>' +
        (k.read_only ? t('开始') : t('开始（需确认）')) + '</button>';
    const blockers = (k.blockers && k.blockers.length)
      ? '<div class="ev-block">' + k.blockers.map(b=>'<div>'+esc(b)+'</div>').join('') + '</div>'
      : '';
    return '<div class="' + cls.join(' ') + '">' +
      '<div class="ev-head"><span class="ev-name">' + esc(k.name) + '</span>' + badge +
        (running && running.kind!==k.key ? '<span class="ev-badge">' + t('排队中') + '</span>' : '') + '</div>' +
      '<div class="ev-script">' + esc(k.script) + '</div>' +
      '<div class="ev-cost">' + esc(k.cost) + '</div>' +
      blockers +
      '<div class="ev-ops">' + ops + '</div>' +
      btn +
      '</div>';
  }).join('');
  ekEvRestoreInputs(keep);
}

function ekEvRenderBaseline(s){
  const b = s.baseline || {};
  if(!b.exists){
    ekRo('#ev-baseline', [{k:'状态', v: b.note || t('尚未冻结基线'), cls:'warn'}]);
    const h = $('#h-ev-baseline');
    if(h) h.textContent = t('命令行执行 python tools/eval_gate.py --set-baseline 冻结；界面不提供，避免误把一次跑坏的结论固化成门禁。');
    return;
  }
  const rows = [
    {k:'冻结时间', v: b.generated_at || ''},
    {k:'样本规模', v: t('{0} 轮 / {1} 份报告', b.n_runs||0, b.n_files||0)},
    {k:'判分器自检', v: t('{0} 项通过', b.selftest_pass||0), cls:'ok'},
    {k:'覆盖率（可判）', v: ekPct(b.coverage_judgeable)}
  ];
  /* 注记只陈述口径，不带方向判断：这里的百分比一律是「判定通过率」（越高越好），
     而 false_recovery 这种维度「越低越好」说的是被测量的东西，不是这个百分比。
     混着说会让人把 100% 读成最差 —— 那比不写注记更误导。 */
  const NOTE = {
    human: t('沙箱内测得：无人值守场景下恒为 0，仅作对照'),
    false_recovery: t('衡量「宣称已恢复、其实没恢复」；这里的百分比是判定通过率，越高越好')
  };
  Object.keys(b.dims||{}).forEach(k=>{
    const v = b.dims[k];
    let label = '· ' + k;
    if(NOTE[k]) label += ' ⓘ';
    rows.push({k:label, v:ekPct(v), cls:''});
  });
  ekRo('#ev-baseline', rows);
  const h = $('#h-ev-baseline');
  if(h) h.textContent = t('带 ⓘ 的维度有口径说明：{0}', Object.keys(NOTE).map(k=>k+'：'+NOTE[k]).join('；'));
}

function ekEvRenderBoards(s){
  const rows = (s.boards||[]).map(b=>({
    k: b.name,
    v: b.available ? (b.source + ' · ' + b.note) : (t('未产出') + '：' + b.note),
    cls: b.available ? 'ok' : 'warn'
  }));
  ekRo('#ev-boards', rows);
}

function ekEvRenderReports(s){
  const box = $('#ev-reports'); if(!box) return;
  const rs = s.reports || [];
  if(!rs.length){ box.innerHTML = '<div class="ro-empty">' + t('docs/eval 下还没有 .json 产物') + '</div>'; return; }
  box.innerHTML = rs.map(r=>{
    const kb = r.size > 1024*1024 ? (Math.round(r.size/1024/102.4)/10)+' MB' : Math.max(1, Math.round(r.size/1024))+' KB';
    const kind = r.kind==='report' ? t('报告') : (r.kind==='shadow' ? t('影子') : (r.kind==='batch' ? t('批次') : t('其它')));
    return '<div class="ev-rep"><span class="k ' + (r.kind==='report'?'report':(r.kind==='shadow'?'shadow':'')) + '">' + esc(kind) + '</span>' +
      '<span class="n">' + esc(r.name) + '</span>' +
      '<span class="m">' + esc(kb) + ' · ' + esc(r.mtime) + '</span></div>';
  }).join('');
}

/* 运行：只读档直接跑；破坏性档先吃 428，弹服务端给的真实风险说明，确认后带 confirm 重发 */
async function ekEvRun(kind, dry){
  const k = ((EK_SET.ev||{}).kinds||[]).find(x=>x.key===kind);
  if(!k) return;
  if(!k.available){ toast(t('这一档当前不可用，先看卡片里的原因'), 'err'); return; }
  const body = {kind};
  if(dry) body.dry_run = true;
  const onlyEl = document.getElementById('ev_only_' + kind);
  if(onlyEl && onlyEl.value.trim()) body.only = onlyEl.value.trim();
  const repEl = document.getElementById('ev_repeat_' + kind);
  if(repEl && parseInt(repEl.value,10) > 0) body.repeat = parseInt(repEl.value,10);
  const awEl = document.getElementById('ev_aw_' + kind);
  if(awEl && awEl.checked) body.allow_writes = true;

  const title = dry ? t('预演：{0}', k.name) : t('开始评测：{0}', k.name);
  const r = await postConfirm('/api/eval/run', body, title, k.cost);
  if(!r) return;                        // 用户取消确认卡
  let j = {}; try{ j = await r.json(); }catch(e){}
  if(!r.ok){
    toast(t('启动失败：') + (j.error || ('HTTP ' + r.status)), 'err');
    if(j.blockers && j.blockers.length) toast(j.blockers.join('；'), 'err');
    await ekEvLoad();
    return;
  }
  if(j.dry_run){
    await askConfirm(t('预演：{0}', k.name), (j.output || t('（无输出）')).slice(0,6000), t('知道了'), false, t('关闭'));
    await ekEvLoad();
    return;
  }
  if(j.started){
    toast(t('已开始：{0}', j.name || k.name));
    if($('#lw-eval') && $('#lw-eval').classList.contains('collapsed')){
      $('#lw-eval').classList.remove('collapsed');
      const b = document.querySelector('[data-log="eval"][data-act="toggle"]');
      if(b) b.textContent = t('折叠');
    }
    await ekEvLoad();
    ekEvPoll(true);
  }
}

async function ekEvStop(){
  const r = await fetch('/api/eval/run',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({kind:'stop'})});
  let j = {}; try{ j = await r.json(); }catch(e){}
  if(j.ok) toast(t('已请求停止：{0}', j.stopped || ''));
  else toast(t('停止失败：') + (j.error || ('HTTP ' + r.status)), 'err');
  await ekEvLoad();
}

/* 轮询只在「设置页 + 评测 tab」可见时开：离开就停，否则后台一直每 2.5 秒打一次 status
   （status 会实探 Python 与读文件，没必要在用户看别的页时也跑）。 */
function ekEvPoll(on){
  if(EK_SET.evTimer){ clearInterval(EK_SET.evTimer); EK_SET.evTimer = null; }
  if(!on) return;
  EK_SET.evTimer = setInterval(async ()=>{
    const p = document.getElementById('panel-settings');
    if(!p || !p.classList.contains('show') || EK_SET.tab !== 'eval'){ ekEvPoll(false); return; }
    const s = await ekEvLoad();
    if(s && !s.running){ ekEvPoll(false); }   // 跑完了就停，别空转
  }, 2500);
}

/* ---------- 设置页交互绑定（唯一一处，全部挂在 document 上） ----------
   为什么挂 document 而不是 #panel-settings / #set-tabs：
   这两个容器都可能在脚本求值时尚不存在（深链、脚本插在 body 前、将来改成懒加载），
   一次 getElementById 拿到 null 就等于整页按钮永久失效，而且报错是静默的
   ——页面看着正常、点了毫无反应，最难查。document 一直存在，用 closest 限定作用域同样安全。
   注意这里是**唯一**的委托：同一批按钮不能绑两遍，否则一次点击会发两次请求。 */
(function(){
  ekDevBindSliders();          // 滑块 ↔ 数字框双向，只绑一次
  document.addEventListener('click', ev=>{
    /* 1) 设置页 tab */
    const tab = ev.target.closest('#set-tabs .cfg-tab');
    if(tab){ ekSetPane(tab.dataset.setTab); return; }
    /* 2) 评测档按钮（卡内动态生成，只能委托） */
    const evb = ev.target.closest('#ev-kinds [data-ev]');
    if(evb){
      if(evb.dataset.ev === 'stop') ekEvStop();
      else ekEvRun(evb.dataset.kind, evb.dataset.ev === 'dry');
      return;
    }
    if(!ev.target.closest('#panel-settings')) return;
    /* 3) 目录选择 */
    const pick = ev.target.closest('[data-spick]');
    if(pick){ ekSetPick(pick); return; }
    /* 4) 普通按钮按 id 分派 */
    const id = ev.target.id;
    if(id === 'btn-set-save') ekSetSaveBase();
    else if(id === 'btn-set-reload') ekSetLoadBase();
    else if(id === 'btn-s-weather') ekSetWeather();
    else if(id === 'btn-sd-save') ekSetSaveDev();
    else if(id === 'btn-sd-reset') ekSetResetDev();
    else if(id === 'btn-ev-refresh') ekEvLoad();
    else if(id === 'btn-sd-breaker') ekSetBreakerReset();
  });
})();

async function ekSetBreakerReset(){
  if(!await askConfirm(t('重置断路器'),
    t('立刻放行一次恢复探针。只有确认底层环境已经恢复时才这么做——否则等于让同一个起不来的服务再被重启一轮。'),
    t('重置'))) return;
  try{
    const r = await post('/api/program/breaker-reset', {});
    if(r && r.ok){
      toast(t('断路器已重置'));
      const h = $('#h-sd-breaker');
      if(h) h.textContent = t('已重置（{0}）', new Date().toLocaleTimeString());
    }else{
      toast(t('重置失败'), 'err');
    }
  }catch(e){ toast(t('网络错误：')+e, 'err'); }
}
